package stdio

// Identity is the canonical identity stamped on every observation
// produced by an ingester.
type Identity struct {
	Service     string
	Environment string
}
