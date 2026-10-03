package main

import (
	"log"
	"net"
	"strconv"
	"strings"
)

// answerDiscovery lets players find the server without typing its address:
// a player broadcasts "S3HIFI?" to UDP port <listen port> and gets "S3HIFI <tcp port>" back.
func answerDiscovery(listen string) {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return
	}
	port, _ := strconv.Atoi(portStr)
	pc, err := net.ListenPacket("udp4", ":"+portStr)
	if err != nil {
		log.Printf("discovery: %v (players will need the server address typed in)", err)
		return
	}
	buf := make([]byte, 64)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(buf[:n])) == "S3HIFI?" {
			pc.WriteTo([]byte("S3HIFI "+strconv.Itoa(port)), from)
		}
	}
}
