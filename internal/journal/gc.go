package journal

// RetentionAssessment is diagnostic only. Evidence publication and identity
// readers exist. Only locked maintenance can authorize deletion; forward floors
// and this unvalidated inventory cannot.
type RetentionAssessment struct {
	Segment  uint64 `json:"segment"`
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

// AssessRetention reports discovered segments without mutating any state.
// Discovery is not topology validation; even an incomplete directory grants no
// eligibility. This API deliberately has no future-certificate override.
func AssessRetention(eventsDir string) ([]RetentionAssessment, error) {
	segments, err := DiscoverSegments(eventsDir)
	if err != nil {
		return nil, err
	}
	result := make([]RetentionAssessment, 0, len(segments))
	for _, segment := range segments {
		result = append(result, RetentionAssessment{Segment: segment,
			Reason: "requires_locked_maintenance_validation"})
	}
	return result, nil
}
