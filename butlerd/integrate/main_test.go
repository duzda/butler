package integrate

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/onsi/gocleanup"
	"github.com/pkg/errors"
	"github.com/sourcegraph/jsonrpc2"
)

var secret = strings.Repeat("dummy", 58)
var address string
var cancelButler context.CancelFunc

var (
	butlerPath = flag.String("butlerPath", "", "path to butler binary to test")
)

func TestMain(m *testing.M) {
	flag.Parse()

	onCi := os.Getenv("CI") != ""

	if !onCi {
		*butlerPath = "butler"
	}

	if *butlerPath == "" {
		if onCi {
			os.Exit(0)
		}
		gmust(errors.New("Not running (--butlerPath must be specified)"))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelButler = cancel

	bExec := exec.CommandContext(ctx, *butlerPath, "daemon", "-j", "--dbpath", "file::memory:?cache=shared")
	stdin, err := bExec.StdinPipe()
	gmust(err)

	stdout, err := bExec.StdoutPipe()
	gmust(err)

	bExec.Stderr = os.Stderr
	gmust(bExec.Start())

	go func() {
		gmust(bExec.Wait())
	}()

	addrChan := make(chan string)

	go func() {
		s := bufio.NewScanner(stdout)
		for s.Scan() {
			line := s.Text()

			im := make(map[string]interface{})
			err := json.Unmarshal([]byte(line), &im)
			if err != nil {
				log.Printf("butler => %s", line)
				continue
			}

			typ := im["type"].(string)
			switch typ {
			case "butlerd/secret-request":
				log.Printf("Sending secret")
				_, err = stdin.Write([]byte(fmt.Sprintf(`{"type": "butlerd/secret-result", "secret": %#v}%s`, secret, "\n")))
				gmust(err)
			case "butlerd/listen-notification":
				addrChan <- im["address"].(string)
			case "log":
				log.Printf("[butler] %s", im["message"].(string))
			default:
				gmust(errors.Errorf("unknown butlerd request: %s", typ))
			}
		}
	}()

	address = <-addrChan

	// keep a main connection going so it doesn't shut down
	connectEx(log.Printf)

	gocleanup.Exit(m.Run())
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		cancelButler()
		if je, ok := errors.Cause(err).(*jsonrpc2.Error); ok {
			if je.Data != nil {
				bs := []byte(*je.Data)
				intermediate := make(map[string]interface{})
				jErr := json.Unmarshal(bs, &intermediate)
				if jErr != nil {
					t.Errorf("could not Unmarshal json-rpc2 error data: %v", jErr)
					t.Errorf("data was: %s", string(bs))
				} else {
					t.Errorf("json-rpc2 full stack:\n%s", intermediate["stack"])
				}
			}
		}
		t.Fatalf("%+v", err)
	}
}

func gmust(err error) {
	if err != nil {
		cancelButler()
		log.Printf("%+v", errors.WithStack(err))
		gocleanup.Exit(1)
	}
}
