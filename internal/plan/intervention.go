package plan

import "github.com/iamseth/tao/internal/intervention"

// EventTypeInterventionObserved records an accepted intervention when no existing
// event describes it. It is diagnostic only, never lifecycle or recovery evidence.
const EventTypeInterventionObserved = "intervention_observed"

// WithInterventionAttribution copies an event and stamps version 1 diagnostic
// attribution. It neither validates nor changes lifecycle state.
func WithInterventionAttribution(event Event, actor, trigger string) Event {
	event.Actor, event.Trigger = intervention.Normalize(actor, trigger)
	event.AttributionVersion = 1
	return event
}

// WithVia copies an event and records an optional bounded provenance label
// describing who or what relayed the action. The label is set only when
// intervention.ParseVia accepts it and yields a nonempty result; otherwise
// Via stays empty, meaning a direct action. It never changes actor, trigger,
// or lifecycle state.
func WithVia(event Event, via string) Event {
	if label, err := intervention.ParseVia(via); err == nil && label != "" {
		event.Via = label
	}
	return event
}
