package usage

type AcceptedEvent struct {
	Event
	Cost                                    *string
	ModelLabel, UpstreamLabel, VersionLabel string
}

type Result struct {
	Inserted   []AcceptedEvent
	Duplicates int
}
