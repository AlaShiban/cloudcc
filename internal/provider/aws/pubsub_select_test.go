package aws

import (
	"strings"
	"testing"
)

// req builds a requirement set from the defaults with the named fields
// changed, so each case reads as "the same topic, except ...".
func req(mutate func(*TopicRequirements)) TopicRequirements {
	r := DefaultTopicRequirements()
	mutate(&r)
	return r
}

// The table. Every row is a different service, and the difference between them
// is behaviour rather than price: SNS cannot replay, SQS cannot fan out, FIFO
// costs throughput. Getting a row wrong means a program that compiles and then
// loses ordering, or messages, under load.
func TestTheRequirementsChooseTheService(t *testing.T) {
	for name, tc := range map[string]struct {
		req  TopicRequirements
		want string
	}{
		"a bare topic fans out": {
			DefaultTopicRequirements(), TopicSNS,
		},
		"one subscriber, unordered": {
			req(func(r *TopicRequirements) { r.Subscribers = "one" }), TopicSQS,
		},
		"one subscriber, ordered by key": {
			req(func(r *TopicRequirements) {
				r.Subscribers = "one"
				r.Ordering = "key"
			}), TopicSQSFifo,
		},
		"one subscriber, exactly once": {
			req(func(r *TopicRequirements) {
				r.Subscribers = "one"
				r.Ordering = "key"
				r.Delivery = "exactly_once"
			}), TopicSQSFifo,
		},
		"many subscribers, totally ordered": {
			req(func(r *TopicRequirements) { r.Ordering = "total" }), TopicSNSFifo,
		},
		"replay wins over everything": {
			req(func(r *TopicRequirements) {
				r.Replay = true
				r.Ordering = "key"
			}), TopicKinesis,
		},
		"replay with one subscriber is still a stream": {
			req(func(r *TopicRequirements) {
				r.Replay = true
				r.Subscribers = "one"
			}), TopicKinesis,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SelectTopicBacking(tc.req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Type != tc.want {
				t.Errorf("resolved to %s, want %s (because: %s)", got.Type, tc.want, got.Because)
			}
			if got.Because == "" {
				t.Error("a choice with no reason is unreviewable in a plan")
			}
		})
	}
}

// A requirement set no service can meet must say which constraint to relax.
// "No backing found" would leave the author guessing at a table they cannot
// see, and guessing usually means dropping the requirement that mattered.
func TestUnsatisfiableRequirementsNameTheConstraint(t *testing.T) {
	for name, tc := range map[string]struct {
		req      TopicRequirements
		mentions []string
	}{
		"exactly once without ordering": {
			req(func(r *TopicRequirements) { r.Delivery = "exactly_once" }),
			[]string{"exactly-once", "ordering", "idempotent"},
		},
		"total order with replay": {
			req(func(r *TopicRequirements) {
				r.Ordering = "total"
				r.Replay = true
			}),
			[]string{"single-shard", `ordering="key"`},
		},
		"long retention without replay": {
			req(func(r *TopicRequirements) { r.RetentionHours = 24 * 30 }),
			[]string{"336", "replay=True"},
		},
		"a message too big for a queue": {
			req(func(r *TopicRequirements) { r.MaxMessageKB = 1024 }),
			[]string{"claim", "256"},
		},
		"a typo in an enum": {
			req(func(r *TopicRequirements) { r.Ordering = "ordered" }),
			[]string{"ordering", "key, none, total"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := SelectTopicBacking(tc.req)
			if err == nil {
				t.Fatal("expected an error; approximating here means a topic that " +
					"silently drops a guarantee")
			}
			for _, want := range tc.mentions {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message should mention %q:\n%s", want, err)
				}
			}
		})
	}
}

// A bigger message is fine on a stream, which holds 1 MB. The limit is a
// property of the chosen service, so it can only be checked after choosing.
func TestTheSizeLimitFollowsTheChosenService(t *testing.T) {
	stream := req(func(r *TopicRequirements) {
		r.Replay = true
		r.MaxMessageKB = 512
	})
	if _, err := SelectTopicBacking(stream); err != nil {
		t.Errorf("512 KB fits a Kinesis record: %v", err)
	}

	queue := req(func(r *TopicRequirements) { r.MaxMessageKB = 512 })
	if _, err := SelectTopicBacking(queue); err == nil {
		t.Error("512 KB does not fit SNS; that should be refused")
	}
}

// A type named in cloudcc.yaml is checked rather than obeyed. Everywhere else
// in cloudcc the file is the stronger layer, because the choice is between
// variants that behave alike. Here they do not.
func TestAConfiguredTypeIsCheckedNotObeyed(t *testing.T) {
	ordered := req(func(r *TopicRequirements) { r.Ordering = "total" })

	if err := TopicSatisfies(TopicSNSFifo, ordered); err != nil {
		t.Errorf("sns_fifo satisfies a totally-ordered topic: %v", err)
	}

	err := TopicSatisfies(TopicSNS, ordered)
	if err == nil {
		t.Fatal("plain SNS cannot order messages; configuring it should be an error")
	}
	for _, want := range []string{"sns_fifo", "cloudcc.yaml", "order"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should mention %q:\n%s", want, err)
		}
	}
}

// The default has to stay SNS, because every topic written before this feature
// existed said nothing at all -- and a compiler that changed what those meant
// would be replacing live resources on an unrelated recompile.
func TestABareTopicStillMeansSNS(t *testing.T) {
	got, err := SelectTopicBacking(DefaultTopicRequirements())
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TopicSNS {
		t.Errorf("a topic with no stated requirements resolved to %s, want sns", got.Type)
	}
}
