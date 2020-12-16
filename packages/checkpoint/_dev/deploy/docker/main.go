package main

import (
	"bufio"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"time"
)

var (
	dest  string
	input string
)

func init() {
	flag.StringVar(&dest, "dest", "localhost:514", "destination tcp address")
	flag.StringVar(&input, "i", "", "input file")
}

func main() {
	log.SetFlags(0)
	flag.Parse()

	var err error
	var tcpAddr *net.TCPAddr
	for {
		tcpAddr, err = net.ResolveTCPAddr("tcp", dest)
		if err != nil {
			log.Println(err)
			time.Sleep(time.Second)
			continue
		}
		break
	}

	var conn *net.TCPConn
	for {
		conn, err = net.DialTCP("tcp", nil, tcpAddr)
		if err != nil {
			log.Println(err)
			time.Sleep(time.Second)
			continue
		}
		break
	}
	defer conn.Close()

	out := bufio.NewWriter(conn)
	defer out.Flush()

	f, err := os.Open(input)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if _, err = io.Copy(out, f); err != nil {
		log.Fatal(err)
	}
}
