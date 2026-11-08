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
