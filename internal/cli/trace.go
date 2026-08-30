package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// The trace is the answer to "what did my program actually do?", which is a
// question neither the source nor the response to a request answers on its
// own. A compiled unit talks to resources you did not name, through bindings
// you did not write, and when it misbehaves the useful evidence is the
// sequence of calls it made at its stores -- not a stack trace, and not a log
// line somebody remembered to add.
//
// Turning it on is one environment variable, in either half:
//
//	CLOUDCC_TRACE=1 python -m uvicorn api:app        # as written
//	CLOUDCC_TRACE=1 node index.mjs                   # or the compiled copy
//
// Events go to stderr, tagged, so they survive a Lambda (where they reach
// CloudWatch) and interleave harmlessly with the application's own logging.
// This command turns that stream into something worth reading, and -- with
// --diff -- compares two of them.
//
// Which is the same comparison tests/e2e/examples.sh makes on every example:
// two runs that traced identically did the same work, whatever they answered.

// traceMarker must match the tracer in both runtimes.
const traceMarker = "##cloudcc-trace##"

type traceEvent struct {
	Kind string          `json:"kind"`
	ID   string          `json:"id"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
	Ret  json.RawMessage `json:"ret,omitempty"`
	Err  string          `json:"err,omitempty"`
}

// resource is how an event is addressed: the logical id the program gave it in
// persist().
//
// Never the physical name -- that legitimately differs between a local run and
// a deployed one, and keying on it would make every comparison fail for a
// reason that is not a defect.
//
// And never the capability either. A compiled unit knows its capability
// literally, while an uncompiled one infers it from the client's type, which
// in JavaScript cannot be made reliable: a `pg.Pool` instance reports
// `BoundPool` and an ioredis client reports `EventEmitter`. `kind` stays in
// the event because it is worth reading; it is not worth comparing.
func (e traceEvent) resource() string { return e.ID }

func (e traceEvent) line() string {
	var b strings.Builder
	b.WriteString(e.Op)
	if len(e.Args) > 0 {
		b.WriteString(" args=")
		b.Write(e.Args)
	}
	if len(e.Ret) > 0 {
		b.WriteString(" ret=")
		b.Write(e.Ret)
	}
	if e.Err != "" {
		b.WriteString(" err=")
		b.WriteString(e.Err)
	}
	return b.String()
}

// readTrace pulls the tagged lines out of a stream that also carries ordinary
// application output, which is the normal case: the tracer shares stderr with
// whatever the program logs.
func readTrace(r io.Reader) ([]traceEvent, int, error) {
	var events []traceEvent
	malformed := 0

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		i := strings.Index(line, traceMarker)
		if i < 0 {
			continue
		}
		payload := strings.TrimSpace(line[i+len(traceMarker):])
		var e traceEvent
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			// Counted and reported rather than skipped. A truncated line
			// dropped in silence would make a run look as though it did less
			// work than it did.
			malformed++
			continue
		}
		events = append(events, e)
	}
	return events, malformed, scanner.Err()
}

// groupByResource keeps each resource's events in the order they happened.
//
// Order is preserved within a resource -- which is where read-your-write bugs
// live -- and not across them: two independent stores touched in either order
// are not a behavioural difference.
func groupByResource(events []traceEvent) ([]string, map[string][]string) {
	byResource := map[string][]string{}
	for _, e := range events {
		byResource[e.resource()] = append(byResource[e.resource()], e.line())
	}
	names := make([]string, 0, len(byResource))
	for name := range byResource {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, byResource
}

func openTrace(path string, stdin io.Reader) (io.ReadCloser, error) {
	if path == "-" || path == "" {
		return io.NopCloser(stdin), nil
	}
	return os.Open(path)
}

func loadTrace(path string, stdin io.Reader) ([]traceEvent, int, error) {
	f, err := openTrace(path, stdin)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	return readTrace(f)
}

func newTraceCommand() *cobra.Command {
	var diffAgainst string

	cmd := &cobra.Command{
		Use:   "trace [file]",
		Short: "Read a CLOUDCC_TRACE stream, or compare two of them",
		Long: "Read what a program did at its cloud seams.\n\n" +
			"Run either half with CLOUDCC_TRACE=1 and it writes one tagged line per\n" +
			"call through a persisted client -- the operation, the logical id from\n" +
			"persist(), the arguments and the result. This reads that stream, from a\n" +
			"file or stdin, and groups it by resource.\n\n" +
			"With --diff it compares two traces. Two runs that traced identically did\n" +
			"the same work, which is a stronger claim than answering alike: a program\n" +
			"that writes to the wrong store, drops a publish or reads a secret that\n" +
			"resolved to \"\" can answer every request identically.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			w := cmd.OutOrStdout()

			events, malformed, err := loadTrace(path, cmd.InOrStdin())
			if err != nil {
				return err
			}

			if diffAgainst == "" {
				names, byResource := groupByResource(events)
				kinds := map[string]string{}
				for _, e := range events {
					if _, seen := kinds[e.resource()]; !seen && e.Kind != "" && e.Kind != "unknown" {
						kinds[e.resource()] = e.Kind
					}
				}
				for _, name := range names {
					// Only here. Reading one trace, the capability is useful
					// context; comparing two, it is an inference on one side
					// against a literal on the other -- see resource().
					if kind := kinds[name]; kind != "" {
						fmt.Fprintf(w, "== %s (%s)\n", name, kind)
					} else {
						fmt.Fprintf(w, "== %s\n", name)
					}
					for _, line := range byResource[name] {
						fmt.Fprintf(w, "   %s\n", line)
					}
				}
				if malformed > 0 {
					fmt.Fprintf(w, "== %d malformed line(s) skipped\n", malformed)
				}
				if len(events) == 0 {
					// Zero events is not "nothing happened", it is far more
					// often "tracing was never on". Saying so beats printing
					// nothing and letting it read as a clean run.
					fmt.Fprintln(w, "no trace events found -- was CLOUDCC_TRACE set for the run?")
				}
				return nil
			}

			other, otherMalformed, err := loadTrace(diffAgainst, nil)
			if err != nil {
				return err
			}
			differences := diffTraces(w, events, other)
			if malformed+otherMalformed > 0 {
				fmt.Fprintf(w, "note: %d malformed line(s) were skipped\n", malformed+otherMalformed)
			}
			if differences > 0 {
				return fmt.Errorf("the two runs did different work: %d resource(s) differ", differences)
			}
			fmt.Fprintln(w, "the two runs did the same work")
			return nil
		},
	}

	cmd.Flags().StringVar(&diffAgainst, "diff", "",
		"compare against another trace file; exits non-zero if they differ")
	return cmd
}

// diffTraces reports, per resource, where two runs diverge. It returns the
// number of resources that differ.
func diffTraces(w io.Writer, a, b []traceEvent) int {
	namesA, byA := groupByResource(a)
	namesB, byB := groupByResource(b)

	seen := map[string]bool{}
	var all []string
	for _, n := range append(append([]string{}, namesA...), namesB...) {
		if !seen[n] {
			seen[n] = true
			all = append(all, n)
		}
	}
	sort.Strings(all)

	differing := 0
	for _, name := range all {
		left, right := byA[name], byB[name]
		if equalLines(left, right) {
			continue
		}
		differing++
		fmt.Fprintf(w, "== %s\n", name)
		for i := 0; i < len(left) || i < len(right); i++ {
			switch {
			case i >= len(right):
				fmt.Fprintf(w, "  -%s\n", left[i])
			case i >= len(left):
				fmt.Fprintf(w, "  +%s\n", right[i])
			case left[i] != right[i]:
				fmt.Fprintf(w, "  -%s\n", left[i])
				fmt.Fprintf(w, "  +%s\n", right[i])
			default:
				fmt.Fprintf(w, "   %s\n", left[i])
			}
		}
	}
	return differing
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
