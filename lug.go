package lug

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	DefaultMaxConn = 16
	DialTimeout    = 10 * time.Second
	PollTimeout    = 2 * time.Millisecond
	ResolveTimeout = 2 * time.Second
	IPProtocol     = "ip4"
)

type cConn struct {
	c net.Conn
	t time.Time
}

type iPool struct {
	l    *lug
	n    atomic.Int32
	pool chan cConn
	addr string
}

type hPool struct {
	l    *lug
	mu   sync.RWMutex
	loc  int
	pool []*iPool
	host string
	port string
	stop chan struct{}
}

type lug struct {
	mu              sync.RWMutex
	maxc            int
	connIdleTimeout time.Duration
	shutdownTimeout time.Duration
	readTimeout     time.Duration
	writeTimeout    time.Duration
	resolver        *net.Resolver
	pool            map[string]*hPool
}

// lug is an HTTP transport that maintains a pool of persistent connections to multiple hosts.
// it only supports HTTP/1.1 and does not support HTTPS
// it only supports HTTP/1.1 keep-alive connections
func NewLug(
	connIdleTimeout time.Duration,
	shutdownTimeout time.Duration,
	readTimeout time.Duration,
	writeTimeout time.Duration,
	maxConn int,
) (*lug, func()) {
	l := &lug{
		mu:              sync.RWMutex{},
		maxc:            maxConn,
		connIdleTimeout: connIdleTimeout,
		shutdownTimeout: shutdownTimeout,
		readTimeout:     readTimeout,
		writeTimeout:    writeTimeout,
		resolver:        net.DefaultResolver,
		pool:            make(map[string]*hPool),
	}
	return l, l.close
}

func (l *lug) close() {
	wg := sync.WaitGroup{}
	for _, p := range l.pool {
		wg.Go(p.close)
	}
	wg.Wait()
}

func (l *lug) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Connection", "keep-alive")

	var req bytes.Buffer
	r.Write(&req)

	var res *http.Response
	con, err, clf := l.getConnection(r.Host)
	defer clf(err)
	if err != nil {
		return nil, err
	}
	con.SetWriteDeadline(time.Now().Add(l.writeTimeout))
	_, err = con.Write(req.Bytes())
	if err != nil {
		return nil, err
	}
	con.SetWriteDeadline(time.Time{})
	con.SetReadDeadline(time.Now().Add(l.readTimeout))
	res, err = http.ReadResponse(bufio.NewReader(con), r)
	con.SetReadDeadline(time.Time{})
	return res, err
}

func (l *lug) getConnection(host string) (net.Conn, error, func(error)) {
	l.mu.RLock()
	if pool, ok := l.pool[host]; ok {
		l.mu.RUnlock()
		return pool.getConnection()
	}
	l.mu.RUnlock()
	l.newPool(host)
	return l.getConnection(host)
}

func (l *lug) newPool(host string) {
	l.mu.Lock()
	if _, ok := l.pool[host]; ok {
		l.mu.Unlock()
		return
	}
	hp := strings.Split(host, ":")
	h := hp[0]
	p := ":80"
	if len(hp) > 1 {
		p = ":" + hp[1]
	}
	pool := &hPool{
		l:    l,
		mu:   sync.RWMutex{},
		pool: []*iPool{},
		host: h,
		port: p,
	}
	go pool.probe()
	l.pool[host] = pool
	l.mu.Unlock()
}

func (p *hPool) close() {
	go func() {
		p.stop <- struct{}{}
	}()
	wg := sync.WaitGroup{}
	for _, ip := range p.pool {
		if ip != nil {
			wg.Go(ip.close)
		}
	}
	wg.Wait()
}

func (p *hPool) maintainPool() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), ResolveTimeout)
	defer cancel()
	ips, err := p.l.resolver.LookupIP(ctx, IPProtocol, p.host)
	if err != nil {
		return err
	}
	if len(ips) != len(p.pool) {
		np := make([]*iPool, len(ips))
		if len(p.pool) == 0 {
			for i := 0; i < len(ips); i++ {
				np[i] = &iPool{
					l:    p.l,
					n:    atomic.Int32{},
					pool: make(chan cConn, p.l.maxc),
					addr: ips[i].String(),
				}
			}
			p.loc = 0
			p.pool = np
			return nil
		}

		op := make([]*iPool, len(p.pool))
		copy(op, p.pool)
		for i := 0; i < len(ips); i++ {
			ip := ips[i].String()
			for j := 0; j < len(p.pool); j++ {
				if ip == p.pool[j].addr {
					np[i] = p.pool[j]
					op[j] = nil
					break
				}
			}
			np[i] = &iPool{
				l:    p.l,
				n:    atomic.Int32{},
				pool: make(chan cConn),
				addr: ip,
			}
		}
		p.loc = 0
		p.pool = np
		for _, xp := range op {
			if xp != nil {
				go xp.close()
			}
		}
	}
	return nil
}

func (p *hPool) probe() {
	for {
		select {
		case <-p.stop:
			return
		case <-time.After(1 * time.Second):
			p.maintainPool()
		}
	}
}

func (p *hPool) moveLoc() {
	p.mu.RLock()
	nloc := p.loc + 1
	if nloc >= len(p.pool) {
		nloc = 0
	}
	if p.pool[nloc].n.Load() <= p.pool[p.loc].n.Load() {
		p.loc = nloc
	}
	p.mu.RUnlock()
}

func (p *hPool) getConnection() (net.Conn, error, func(error)) {
	if len(p.pool) > 0 {
		con, err, fun := p.pool[p.loc].getConnection(p.port)
		go p.moveLoc()
		return con, err, fun
	}
	if err := p.maintainPool(); err != nil {
		return nil, err, func(error) {}
	}
	con, err, fun := p.pool[p.loc].getConnection(p.port)
	go p.moveLoc()
	return con, err, fun
}

func (p *iPool) close() {
	ctx, cancel := context.WithTimeout(context.Background(), p.l.shutdownTimeout)
	defer cancel()
	ch := make(chan struct{})
	go func() {
		for p.n.Load() > 0 {
		}
		for len(p.pool) > 0 {
			c := <-p.pool
			c.c.Close()
		}
		ch <- struct{}{}
	}()
	select {
	case <-ctx.Done():
	case <-ch:
	}
	go func() {
		defer func() {
			recover()
		}()
		close(p.pool)
	}()
}

func (p *iPool) getConnection(port string) (net.Conn, error, func(error)) {
	p.n.Add(1)
	con, ok := p.poll()
	if ok {
		return con, nil, func(e error) {
			p.n.Add(-1)
			if e == nil && len(p.pool) < p.l.maxc {
				p.pool <- cConn{c: con, t: time.Now()}
			}
		}
	}
	con, err := net.DialTimeout("tcp", p.addr+port, DialTimeout)
	if err != nil {
		return nil, err, func(error) {
		}
	}
	return con, nil, func(e error) {
		p.n.Add(-1)
		if e == nil && len(p.pool) < p.l.maxc {
			p.pool <- cConn{c: con, t: time.Now()}
		} else {
			con.Close()
		}
	}
}

func (p *iPool) waitTimout() time.Duration {
	if p.n.Load() < int32(p.l.maxc) {
		return PollTimeout
	}
	return PollTimeout + time.Duration(p.n.Load())*time.Millisecond
}

func (p *iPool) poll() (net.Conn, bool) {
	ctx, cxl := context.WithTimeout(context.Background(), p.waitTimout())
	defer cxl()
	for {
		select {
		case cx := <-p.pool:
			if time.Since(cx.t) < p.l.connIdleTimeout {
				return cx.c, true
			} else {
				cx.c.Close()
			}
		case <-ctx.Done():
			return nil, false
		}
	}
}
