package httpapi

// The log streams' named events and what their done events can say. tygo
// generates these for the SPA (web/app/src/api/generated/httpapi.ts), so
// renaming one here breaks the SPA's type check instead of its streams.

// StreamEvent names a Server-Sent Event of a log stream. Output itself
// comes in unnamed "message" events.
type StreamEvent string

const (
	// StreamOmitted comes first when a Deployment Log stream skipped the
	// start of the log; its data is how many bytes it left out.
	StreamOmitted StreamEvent = "omitted"
	// StreamDone ends a stream for good: the browser must not reconnect.
	// Its data is a Deployment Log's final Deployment status, or one of
	// the StreamEnd reasons.
	StreamDone StreamEvent = "done"
)

// StreamEnd is a done event's data when a stream ends for a reason other
// than its Deployment finishing.
type StreamEnd string

const (
	// AppLogReplaced: a new Deployment replaced the container the App Log
	// was following.
	AppLogReplaced StreamEnd = "replaced"
	// AppLogNoLiveDeployment: the App has no live Deployment, so its App
	// Log has no container to follow.
	AppLogNoLiveDeployment StreamEnd = "no-live-deployment"
)

// message is the unnamed event that carries a log's output.
const message StreamEvent = ""
