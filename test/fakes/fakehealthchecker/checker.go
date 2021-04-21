package fakehealthchecker

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/spiffe/spire/pkg/common/health"
)

type Checker struct {
	checkables map[string]health.Checkable
}

var _ health.Checker = (*Checker)(nil)

func New() *Checker {
	return &Checker{
		checkables: make(map[string]health.Checkable),
	}
}

func (c *Checker) AddCheck(name string, checkable health.Checkable) error {
	if _, ok := c.checkables[name]; ok {
		return fmt.Errorf("check %q has already been added", name)
	}
	c.checkables[name] = checkable
	return nil
}

func (c *Checker) LiveState() (bool, interface{}) {
	live, _, details, _ := c.checkStates()
	return live, details
}

func (c *Checker) ReadyState() (bool, interface{}) {
	_, ready, _, details := c.checkStates()
	return ready, details
}

func (c *Checker) ListenAndServe(ctx context.Context) error {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Hello, client")
	}))
	defer ts.Close()

	_, err := http.Get(ts.URL)
	if err != nil {
		log.Fatal(err)
	}

	return err
}

func (c *Checker) RunChecks() map[string]health.State {
	results := make(map[string]health.State)

	for name, checkable := range c.checkables {
		results[name] = checkable.CheckHealth()
	}

	return results
}

func (c *Checker) checkStates() (bool, bool, interface{}, interface{}) {
	isLive, isReady := true, true
	liveDetails := make(map[string]interface{})
	readyDetails := make(map[string]interface{})

	for subName, subState := range c.checkables {
		state := subState.CheckHealth()
		if !state.Live {
			isLive = false
		}

		if !state.Ready {
			isReady = false
		}

		liveDetails[subName] = state.LiveDetails
		readyDetails[subName] = state.ReadyDetails
	}

	return isLive, isReady, liveDetails, readyDetails
}
