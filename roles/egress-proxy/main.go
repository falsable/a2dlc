// Command egress-proxy is the allowlist proxy of a2dlc's provider network. It reads A2DLC_EGRESS_ALLOW (comma-separated
// hostnames) and serves HTTP CONNECT on :3128. With -healthcheck it dials its own port and exits 0 when it answers;
// the launcher uses that to wait until the proxy is up before it starts the role.
package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

const listenAddr = ":3128"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-healthcheck" {
		c, err := net.DialTimeout("tcp", "127.0.0.1"+listenAddr, 2*time.Second)
		if err != nil {
			log.Print(err)
			os.Exit(1)
		}
		c.Close()
		return
	}
	allow, err := ParseAllow(os.Getenv("A2DLC_EGRESS_ALLOW"))
	if err != nil {
		log.Print(err)
		os.Exit(2)
	}
	log.Printf("egress-proxy listening on %s with %d allowed hosts", listenAddr, len(allow))
	srv := &http.Server{Addr: listenAddr, Handler: NewProxy(allow), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
