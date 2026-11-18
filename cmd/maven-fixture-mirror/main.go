// maven-fixture-mirror serves the recorded Maven Central and Gradle plugin
// portal fixture on a loopback port, so Maven and Gradle end-to-end scripts run
// without outbound network. With MAVEN_CENTRAL_FIXTURE_RECORD=1 a miss is
// fetched from the real upstream and stored (the record-maven-fixtures workflow).
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/mlops-ca/notarx/internal/mavenfixture"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "loopback listen address")
	dir := flag.String("dir", mavenfixture.DefaultDir(), "fixture directory")
	flag.Parse()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	// The first line names the mirror URL; scripts read it.
	fmt.Printf("http://%s\n", listener.Addr())
	_ = os.Stdout.Sync()
	server := &http.Server{Handler: mavenfixture.New(*dir, mavenfixture.RecordMode(), log.Printf), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(server.Serve(listener))
}
