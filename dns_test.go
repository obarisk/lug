package lug

import (
	"context"
	"fmt"
	"math/rand"
	"net"

	"golang.org/x/net/dns/dnsmessage"
)

func fakeDNSServer(ctx context.Context) (net.Addr, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	go func() {
		defer conn.Close()
		for {
			buf := make([]byte, 512)
			_, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				fmt.Printf("error reading from UDP: %v", err)
				continue
			}
			go handleDNSRequest(conn, addr, buf)
		}
	}()
	return conn.LocalAddr(), nil
}

func ips(name dnsmessage.Name) []dnsmessage.Resource {
	v0 := dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{
			Name:  name,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
			TTL:   300,
		},
		Body: &dnsmessage.AResource{
			A: [4]byte{127, 0, 0, 1},
		},
	}
	rs := []dnsmessage.Resource{v0}
	n := rand.Intn(5)
	for i := 0; i <= n; i++ {
		vx := dnsmessage.Resource{
			Header: v0.Header,
			Body: &dnsmessage.AResource{
				A: [4]byte{127, 0, 0, byte(2 + i)},
			},
		}
		rs = append(rs, vx)
	}
	return rs
}

func handleDNSRequest(conn *net.UDPConn, addr *net.UDPAddr, buf []byte) {
	var msg dnsmessage.Message
	if err := msg.Unpack(buf); err != nil {
		fmt.Printf("failed to unpack DNS message: %v", err)
		return
	}
	if len(msg.Questions) == 0 {
		return
	}
	question := msg.Questions[0]
	if question.Type != dnsmessage.TypeA {
		return
	}
	// Respond with a hardcoded IP address
	response := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:            msg.ID,
			Response:      true,
			Authoritative: true,
		},
		Questions: msg.Questions,
		Answers:   ips(question.Name),
	}

	packed, err := response.Pack()
	if err != nil {
		fmt.Printf("failed to pack DNS response: %v", err)
		return
	}

	if _, err := conn.WriteToUDP(packed, addr); err != nil {
		fmt.Printf("failed to write DNS response: %v ", err)
	}
}
