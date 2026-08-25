package aws

import (
	"fmt"
	"sort"
	"strings"
)

// TopicRequirements is what a program declares about how its messages must
// move. Every field changes the answer, and none of them is a preference.
//
// This is the inversion that makes pub/sub different from storage. For a store
// the library picks the capability and cloudcc.yaml picks the variant: a Redis
// client is ElastiCache or MemoryDB, and both have the same API. A topic has no
// library to ask, and the variants are not interchangeable -- SNS cannot
// replay, SQS cannot fan out, and FIFO everything costs throughput. Choosing by
// hand means knowing all of that; declaring the requirement means the compiler
// has to.
type TopicRequirements struct {
	// Subscribers is "one" or "many". One subscriber means a queue; many means
	// a fan-out, and the difference decides half the table below.
	Subscribers string
	// Ordering is "none", "key" or "total".
	//
	//   none  -- messages may arrive in any order
	//   key   -- messages sharing a key arrive in the order they were sent
	//   total -- every message arrives in the order it was sent, globally
	Ordering string
	// Delivery is "at_least_once" or "exactly_once".
	Delivery string
	// Replay says whether a subscriber can read messages sent before it
	// existed. It is what separates a queue from a stream.
	Replay bool
	// RetentionHours is how long a message is kept. Zero means the service
	// default.
	RetentionHours int
	// MaxMessageKB is the largest message the program will publish.
	MaxMessageKB int
}

// Topic backing services. These are the values `persisted.<id>.type` accepts
// for a pub/sub id, and what Select resolves a requirement set to.
const (
	TopicSNS      = "sns"
	TopicSNSFifo  = "sns_fifo"
	TopicSQS      = "sqs"
	TopicSQSFifo  = "sqs_fifo"
	TopicKinesis  = "kinesis"
	maxSNSPayload = 256  // KB, and the same for SQS
	maxSQSHours   = 336  // 14 days
	maxKinesisKB  = 1024 // 1 MB per record
)

// DefaultTopicRequirements is what a bare Topic() asks for: fan-out, no
// ordering guarantee, at-least-once. It resolves to SNS, which is what a topic
// with no stated requirements has always compiled to.
func DefaultTopicRequirements() TopicRequirements {
	return TopicRequirements{
		Subscribers:  "many",
		Ordering:     "none",
		Delivery:     "at_least_once",
		MaxMessageKB: maxSNSPayload,
	}
}

// TopicChoice is what Select returns: the service, and why.
type TopicChoice struct {
	// Type is the backing service.
	Type string
	// Because explains which requirements forced it, for the plan and for the
	// error when the chosen service is not implemented yet.
	Because string
}

// SelectTopicBacking picks the service that satisfies every requirement, or
// explains which one cannot be met.
//
// It never approximates. A requirement set with no answer is an error naming
// the constraint to relax, because the alternative -- defaulting to SNS and
// letting the differences surface in production -- is a topic that silently
// drops ordering, and that is a bug which reproduces once a week.
func SelectTopicBacking(r TopicRequirements) (TopicChoice, error) {
	if err := validateTopicRequirements(r); err != nil {
		return TopicChoice{}, err
	}

	// Contradictions first, so the message names the pair rather than the
	// service that failed to exist.
	if r.Delivery == "exactly_once" && r.Ordering == "none" {
		return TopicChoice{}, fmt.Errorf(
			"delivery=%q with ordering=%q describes nothing on AWS: exactly-once is a "+
				"property of the FIFO services, and those are ordered by definition. "+
				"Either ask for ordering=\"key\", or accept delivery=\"at_least_once\" "+
				"and make the subscriber idempotent",
			r.Delivery, r.Ordering)
	}
	if r.Ordering == "total" && r.Replay {
		return TopicChoice{}, fmt.Errorf(
			"ordering=\"total\" with replay=True means a single-shard stream, which is a "+
				"throughput ceiling rather than a design. Relax to ordering=\"key\": "+
				"messages sharing a key stay ordered, and the stream can grow")
	}
	if r.RetentionHours > maxSQSHours && !r.Replay {
		return TopicChoice{}, fmt.Errorf(
			"retention_hours=%d exceeds the %d hours a queue can hold. Keeping messages "+
				"longer than that is what a stream is for: set replay=True",
			r.RetentionHours, maxSQSHours)
	}

	choice := chooseTopic(r)

	// Size last, because it depends on which service was chosen.
	if limit := payloadLimit(choice.Type); r.MaxMessageKB > limit {
		return TopicChoice{}, fmt.Errorf(
			"max_message_kb=%d does not fit %s, which holds %d KB. The fix is a claim "+
				"check -- publish the body to a persisted store and send the key -- which "+
				"changes what a failed delivery leaves behind, so it is worth writing "+
				"rather than inserting on your behalf",
			r.MaxMessageKB, choice.Type, limit)
	}
	return choice, nil
}

// chooseTopic is the table. Replay is checked first because it rules out every
// service that does not keep a cursor.
func chooseTopic(r TopicRequirements) TopicChoice {
	switch {
	case r.Replay:
		return TopicChoice{TopicKinesis, "replay=True: only a stream lets a subscriber " +
			"read messages sent before it existed"}

	case r.Subscribers == "one" && r.Ordering == "none":
		return TopicChoice{TopicSQS, "one subscriber and no ordering requirement: a queue " +
			"delivers to exactly one consumer without the throughput cost of FIFO"}

	case r.Subscribers == "one":
		return TopicChoice{TopicSQSFifo, fmt.Sprintf(
			"one subscriber with ordering=%q: a FIFO queue is what orders a single "+
				"consumer's messages", r.Ordering)}

	case r.Ordering == "none":
		return TopicChoice{TopicSNS, "many subscribers and no ordering requirement: " +
			"fan-out, each subscriber with its own queue"}

	default:
		return TopicChoice{TopicSNSFifo, fmt.Sprintf(
			"many subscribers with ordering=%q: a FIFO topic, which can only fan out "+
				"to FIFO queues", r.Ordering)}
	}
}

func payloadLimit(topicType string) int {
	if topicType == TopicKinesis {
		return maxKinesisKB
	}
	return maxSNSPayload
}

// topicEnums are the values each requirement accepts. A typo in one of these
// would otherwise pick a backing silently, which is the failure this whole
// capability is arranged to prevent.
var topicEnums = map[string][]string{
	"subscribers": {"many", "one"},
	"ordering":    {"key", "none", "total"},
	"delivery":    {"at_least_once", "exactly_once"},
}

func validateTopicRequirements(r TopicRequirements) error {
	for field, value := range map[string]string{
		"subscribers": r.Subscribers,
		"ordering":    r.Ordering,
		"delivery":    r.Delivery,
	} {
		if !contains(topicEnums[field], value) {
			return fmt.Errorf("%s=%q is not a value cloudcc knows; it accepts %s",
				field, value, strings.Join(topicEnums[field], ", "))
		}
	}
	if r.RetentionHours < 0 {
		return fmt.Errorf("retention_hours=%d must not be negative", r.RetentionHours)
	}
	if r.MaxMessageKB <= 0 {
		return fmt.Errorf("max_message_kb=%d must be positive", r.MaxMessageKB)
	}
	return nil
}

// TopicSatisfies reports whether a type named in cloudcc.yaml can meet the
// requirements the program declared.
//
// A configured type is checked rather than obeyed. Everywhere else in cloudcc
// the file is the stronger layer, because the choice there is between variants
// with the same behaviour -- ElastiCache or MemoryDB, both Redis. Here the
// variants behave differently, and a file that said `sns` for a topic whose
// code asks for ordering would be asking for messages to be delivered out of
// order. That is not a preference to honour.
func TopicSatisfies(topicType string, r TopicRequirements) error {
	want, err := SelectTopicBacking(r)
	if err != nil {
		return err
	}
	if want.Type == topicType {
		return nil
	}
	return fmt.Errorf(
		"the code asks for a topic that %s, which resolves to %s, but cloudcc.yaml says "+
			"%s. Change the type, or change the requirement -- a topic configured to a "+
			"service that cannot meet its guarantees loses messages, or their order, and "+
			"only under load",
		want.Because, want.Type, topicType)
}

// TopicBackings lists every service a topic can resolve to, sorted.
func TopicBackings() []string {
	out := []string{TopicKinesis, TopicSNS, TopicSNSFifo, TopicSQS, TopicSQSFifo}
	sort.Strings(out)
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
