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

// tuning these parameters depends on your internet conditions and server capabilities
var (
	DefaultMaxIdleConn = int32(16)
	DialTimeout        = 5 * time.Second
	PollTimeout        = 2 * time.Millisecond
	ResolveTimeout     = 5 * time.Second
	ResolveInterval    = 1 * time.Second
	IPProtocol         = "ip4"
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
	resolver        *net.Resolver
	pool            map[string]*hPool
	maxIdleConn     int32
	connIdleTimeout time.Duration
	readTimeout     time.Duration
	writeTimeout    time.Duration
	shutdownTimeout time.Duration
}

// lug is an HTTP transport that maintains a pool of persistent connections to multiple hosts.
// it only supports HTTP/1.1 and does not support HTTPS
// it only supports HTTP/1.1 keep-alive connections
func NewLug(
	connIdleTimeout time.Duration,
	shutdownTimeout time.Duration,
	readTimeout time.Duration,
	writeTimeout time.Duration,
	maxIdleConn int32,
) (*lug, func()) {
	l := &lug{
		mu:              sync.RWMutex{},
		resolver:        net.DefaultResolver,
		pool:            make(map[string]*hPool),
		maxIdleConn:     maxIdleConn,
		connIdleTimeout: connIdleTimeout,
		readTimeout:     readTimeout,
		writeTimeout:    writeTimeout,
		shutdownTimeout: shutdownTimeout,
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
	r.Write(&req) // nolint

	var res *http.Response
	con, err, clf := l.getConnection(r.Host)
	if err != nil {
		go clf(err)
		return nil, err
	}

	con.SetWriteDeadline(time.Now().Add(l.writeTimeout))
	_, err = con.Write(req.Bytes())
	if err != nil {
		go clf(err)
		return nil, err
	}
	con.SetWriteDeadline(time.Time{})

	con.SetReadDeadline(time.Now().Add(l.readTimeout))
	res, err = http.ReadResponse(bufio.NewReader(con), r)
	con.SetReadDeadline(time.Time{})
	go clf(err)

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
		stop: make(chan struct{}),
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
			wg.Go(func() {
				ip.close(p.l.shutdownTimeout)
			})
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
					pool: make(chan cConn, p.l.maxIdleConn),
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
			if np[i] != nil {
				continue
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
				go xp.close(p.l.shutdownTimeout)
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
		case <-time.After(ResolveInterval):
			p.maintainPool()
		}
	}
}

func (p *hPool) moveLoc() {
	if p.mu.TryRLock() {
		ploc, nloc := p.loc-1, p.loc+1
		if nloc >= len(p.pool) {
			nloc = 0
		}
		if ploc < 0 {
			ploc = len(p.pool) - 1
		}
		xloc := nloc
		if p.pool[ploc].n.Load() < p.pool[nloc].n.Load() {
			xloc = ploc
		}
		if p.pool[xloc].n.Load() <= p.pool[p.loc].n.Load() {
			p.loc = xloc
		}
		p.mu.RUnlock()
	}
}

func (p *hPool) getConnection() (net.Conn, error, func(error)) {
	if len(p.pool) > 0 {
		con, err, fun := p.pool[p.loc].getConnection(p.port)
		p.moveLoc()
		return con, err, fun
	}
	if err := p.maintainPool(); err != nil {
		return nil, err, func(error) {}
	}
	con, err, fun := p.pool[p.loc].getConnection(p.port)
	p.moveLoc()
	return con, err, fun
}

func (p *iPool) close(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
			// it's better find a better way to handle closed channel
			recover()
		}()
		close(p.pool)
	}()
}

func (p *iPool) put(c net.Conn) func(error) {
	return func(e error) {
		defer func() {
			// it's better find a better way to handle closed channel
			recover()
		}()
		if p.n.Load() < p.l.maxIdleConn && e == nil {
			p.pool <- cConn{c: c, t: time.Now()}
			p.n.Add(-1)
			return
		}
		c.Close()
		p.n.Add(-1)
	}
}

func (p *iPool) getConnection(port string) (net.Conn, error, func(error)) {
	p.n.Add(1)
	con, ok := p.poll()
	if ok {
		return con, nil, p.put(con)
	}
	con, err := net.DialTimeout("tcp", p.addr+port, DialTimeout)
	if err != nil {
		return nil, err, func(error) {
			p.n.Add(-1)
		}
	}
	return con, nil, p.put(con)
}

func (p *iPool) poll() (net.Conn, bool) {
	for len(p.pool) > 0 {
		cx := <-p.pool
		if time.Since(cx.t) < p.l.connIdleTimeout {
			return cx.c, true
		} else {
			cx.c.Close()
		}
	}
	return nil, false
}
