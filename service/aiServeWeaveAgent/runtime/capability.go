package runtime

type Capability string

type CapabilitySource string

type SupportLevel string

const (
	SupportUnknown     SupportLevel = "unknown"
	SupportSupported   SupportLevel = "supported"
	SupportUnsupported SupportLevel = "unsupported"
)

type CapabilityEvidence struct {
	Capability Capability
	Level      SupportLevel
	Source     CapabilitySource
	Detail     string
}

type CapabilitySet map[Capability]CapabilityEvidence
