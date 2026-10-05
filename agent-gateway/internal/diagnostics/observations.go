package diagnostics

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// These closed populations have no identifiers, dynamic labels, delivery or
// execution ownership. HTTP requests include the separately classified Git subset.
type Protocol uint8

const (
	MCP Protocol = iota
	HTTP
	Connect
	Git
)

type ObservationStage uint8

const (
	AdmissionStage ObservationStage = iota
	ExecutionStage
	RequestStage
)

type Outcome uint8

const (
	Succeeded Outcome = iota
	PrestartFailure
	Failed
	Unknown
	Nonmutation
)

// Observations is bounded process memory independent of both optional sinks.
// Short critical sections only copy/update fixed arrays; no I/O occurs here.
type Observations struct {
	mu     sync.Mutex
	status contract.ExecutionObservations
}

func NewObservations() *Observations { return newObservations(rand.Reader) }

func newObservations(entropy io.Reader) *Observations {
	var identity [16]byte
	epoch := ""
	if _, err := io.ReadFull(entropy, identity[:]); err == nil {
		epoch = hex.EncodeToString(identity[:])
	}
	return &Observations{status: contract.ExecutionObservations{Epoch: epoch, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Coverage: "owner_boundaries", Protocols: [4]contract.ObservedProtocol{{Protocol: "mcp"}, {Protocol: "http"}, {Protocol: "connect"}, {Protocol: "git"}}}}
}
func (o *Observations) add(v *uint64) {
	if *v < maxCount {
		*v++
	} else {
		o.status.Overflow = true
	}
}
func (o *Observations) Request(p Protocol) {
	if o == nil || p > Git {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.add(&o.status.Protocols[p].Requests)
}
func (o *Observations) Execution(p Protocol) {
	if o == nil || p > Git {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.add(&o.status.Protocols[p].Executions)
}
func (o *Observations) Result(p Protocol, result Outcome) {
	if o == nil || p > Git || result > Nonmutation {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.add(&o.status.Protocols[p].Results[result])
}
func (o *Observations) GitReport(report string) {
	if o == nil {
		return
	}
	var i int
	switch report {
	case "reported_success":
		i = 0
	case "reported_failure":
		i = 1
	case "reported_partial":
		i = 2
	default:
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.add(&o.status.Protocols[Git].GitReports[i])
}
func (o *Observations) Latency(p Protocol, stage ObservationStage, elapsed time.Duration) {
	if o == nil || p > Git || stage > RequestStage || elapsed < 0 {
		return
	}
	bucket := 5
	for i, bound := range [...]time.Duration{time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second, 10 * time.Second} {
		if elapsed <= bound {
			bucket = i
			break
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.add(&o.status.Protocols[p].Latency[stage][bucket])
}
func (o *Observations) Status() contract.ExecutionObservations {
	if o == nil {
		return contract.ExecutionObservations{Coverage: "unavailable"}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.status
}
