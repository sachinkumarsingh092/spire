package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/InVisionApp/go-health"
	"github.com/sirupsen/logrus"
	"github.com/spiffe/spire/pkg/common/telemetry"
	"google.golang.org/grpc"
)

const (
	// testDialTimeout is the duration to wait for a test dial
	testDialTimeout = 30 * time.Second

	readyCheckInterval = time.Minute
)

type jsonStatus struct {
	Message string `json:"message"`
	Status  string `json:"status"`
}

// Checker is responsible for running health checks and serving the healthcheck HTTP paths
type Checker struct {
	config Config

	server *http.Server

	hc *health.Health

	live  Liveness
	ready Readiness

	mutex sync.Mutex // Mutex protects non-threadsafe hc

	log logrus.FieldLogger
}

type Liveness interface {
	IsLive() bool
}

type Readiness interface {
	IsReady() bool
}

// TODO:
// * Make 2 helper functions that take in the state as a map and do type attestation to determine wheter it is live or ready.
// * The determination of live and ready will depend on individual subsystem and and we just need to return an opaque interface from
//   Status().
// * The new HTTP handler will take up 3 args: health.Health instance, state map, a high level function that internally uses the live and ready
//	 functions.
// * After the above is done, we will have to use make implement 2 interfaces for every subsytem to check if the system is live or ready and
//   implements the IStatusListener for those systems.

func HealthHandlerFunc(h *health.Health) http.HandlerFunc {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		states, failed, err := h.State()
		if err != nil {
			writeJSONStatus(rw, "error", fmt.Sprintf("Unable to fetch states: %v", err), http.StatusOK)
			return
		}

		msg := "ok"
		statusCode := http.StatusOK

		// There may be an _initial_ delay in display healthcheck data as the
		// healthchecks will only begin firing at "initialTime + checkIntervalTime"
		if len(states) == 0 {
			writeJSONStatus(rw, msg, "Healthcheck spinning up", statusCode)
			return
		}

		if failed {
			msg = "failed"
			statusCode = http.StatusInternalServerError
		}

		checker := Checker{}
		if checker.live.IsLive() {
			fmt.Print("I'm live")
			writeJSONStatus(rw, msg, "Live handler works", statusCode)
		}

		if checker.ready.IsReady() {
			fmt.Print("I'm ready")
			writeJSONStatus(rw, msg, "Ready handler works", statusCode)
		}
	})
}

func live(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
}

func NewChecker(config Config, log logrus.FieldLogger) *Checker {
	hc := health.New()

	var server *http.Server
	// Start HTTP server if ListenerEnabled is true
	if config.ListenerEnabled {
		handler := http.NewServeMux()

		handler.HandleFunc(config.getReadyPath(), HealthHandlerFunc(hc))
		handler.HandleFunc(config.getLivePath(), live)

		server = &http.Server{
			Addr:    config.getAddress(),
			Handler: handler,
		}

		// TODO: Put live and ready interfaces in checker here.

	}

	l := log.WithField(telemetry.SubsystemName, "health")
	hc.StatusListener = &statusListener{log: l}
	hc.Logger = &logadapter{FieldLogger: l}

	return &Checker{config: config, server: server, hc: hc, log: log}
}

// WaitForTestDial tries to create a client connection to the given target
// with a blocking dial and a timeout specified in testDialTimeout.
// Nothing is done with the connection, which is just closed in case it
// is created.
func WaitForTestDial(ctx context.Context, addr *net.UnixAddr) {
	ctx, cancel := context.WithTimeout(ctx, testDialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx,
		addr.String(),
		grpc.WithInsecure(),
		grpc.WithContextDialer(func(ctx context.Context, name string) (net.Conn, error) {
			return net.DialUnix("unix", nil, &net.UnixAddr{
				Net:  "unix",
				Name: name,
			})
		}),
		grpc.WithBlock())
	if err != nil {
		return
	}

	conn.Close()
}

func (c *Checker) AddCheck(name string, checker health.ICheckable) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.hc.AddCheck(&health.Config{
		Name:     name,
		Checker:  checker,
		Interval: readyCheckInterval,
		Fatal:    true,
	})
}

func (c *Checker) ListenAndServe(ctx context.Context) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if err := c.hc.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	if c.config.ListenerEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.log.WithField("address", c.server.Addr).Info("Serving health checks")
			if err := c.server.ListenAndServe(); err != http.ErrServerClosed {
				c.log.WithError(err).Warn("Error serving health checks")
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		if c.server != nil {
			c.server.Close()
		}
	}()

	wg.Wait()

	if err := c.hc.Stop(); err != nil {
		c.log.WithError(err).Warn("Error stopping health checks")
	}

	return nil
}

func writeJSONStatus(rw http.ResponseWriter, status, message string, statusCode int) {
	jsonData, _ := json.Marshal(&jsonStatus{
		Message: message,
		Status:  status,
	})

	writeJSONResponse(rw, statusCode, jsonData)
}

func writeJSONResponse(rw http.ResponseWriter, statusCode int, content []byte) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	rw.WriteHeader(statusCode)
	rw.Write(content)
}
