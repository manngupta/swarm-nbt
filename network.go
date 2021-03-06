package main

import (
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/client"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	httpServerPort = 3443
	udpServerPort  = 6789
	udpClientPort  = 6790
	// maxPollWaitSeconds is the maximum number of seconds to wait
	// between successive tests
	maxPollWaitSeconds = 15
)

var httpTimeout = 5 * time.Second
var udpTimeout = 5 * time.Second
var udpClientTimeout = 5 * time.Second
var icmpMaxRTT = 5 * time.Second

// NetworkTest collects network link information from the local node against all nodes in the
// provided node inventory.
// The following tests are performed:
//		- ICMP RTT
//		- HTTP Timeouts & RTT
//		- UDP Packet Loss & RTT
func NetworkTest(dclient client.CommonAPIClient, nodes []*Node, localNode *Node) error {
	log.Infof("Commencing network test against a cluster of %d nodes", len(nodes))
	log.Infof("Local node address: %s", localNode.Address)

	// Create an error channel
	errChan := make(chan error)

	// Create an ICMP Pinger
	icmpPinger := &ICMPPinger{
		IsManager: localNode.IsManager,
	}
	icmpPinger.Init()

	// Create a UDP Pinger
	udpNodeAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", localNode.Address, udpServerPort))
	if err != nil {
		return err
	}
	udpPinger := &UDPPinger{
		NodeAddr:  udpNodeAddr,
		Timeout:   udpTimeout,
		IsManager: localNode.IsManager,
	}

	// Start a UDP Server on a separate goroutine at port udpServerPort
	go udpPinger.StartUDPServer(errChan)

	// Start an HTTP Server on a separate goroutine at port httpServerPort
	go func(errChan chan<- error) {
		router := mux.NewRouter()
		router.Handle("/metrics", promhttp.Handler())
		srv := &http.Server{
			Handler: router,
			Addr:    fmt.Sprintf(":%d", httpServerPort),
		}
		err := srv.ListenAndServe()
		if err != nil {
			errChan <- err
		}
	}(errChan)

	// Error handling goroutine
	go func(errChan <-chan error) {
		for {
			err := <-errChan
			log.Errorf("Error: %s", err)
		}
	}(errChan)

	httpPinger := &HTTPPinger{
		IsManager: localNode.IsManager,
	}
	// Populate the pingers with the known node inventory
	for _, node := range nodes {
		if node.Address == "127.0.0.1" {
			log.Infof("Skipping local redirect on address %s", node.Address)
			continue
		}
		if node.Hostname == "localhost" {
			log.Info("Skipping local redirect on localhost")
			continue
		}
		log.Infof("Target node hostname: %s, IP Address: %s", node.Hostname, node.Address)
		// Add the IP address to the ICMP pinger
		icmpPinger.AddTarget(node)

		// Create an HTTP URL for the HTTP pinger
		httpPinger.AddTarget(node)

		// Create a target for the UDP pinger
		udpPinger.AddTarget(node)
	}

	// Long-lasting Client loop
	var wg sync.WaitGroup
	for {
		// ICMP Echo
		wg.Add(1)
		go func() {
			defer wg.Done()
			err = icmpPinger.Run()
			if err != nil {
				log.Error(err)
			}
		}()

		// HTTP GET
		wg.Add(1)
		go func() {
			defer wg.Done()
			httpPinger.Run()
		}()

		// UDP Send
		wg.Add(1)
		go func() {
			defer wg.Done()
			udpPinger.Run()
		}()

		wg.Wait()
		time.Sleep(time.Duration(rand.Intn(maxPollWaitSeconds)) * time.Second)
	}

	return nil
}
