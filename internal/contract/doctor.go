package contract

// Severities of a doctor finding (contracts/doctor.schema.json).
const (
	DoctorBlocking = "blocking"
	DoctorWarning  = "warning"
	DoctorInfo     = "info"
)

// Doctor is what `a2dlc doctor --json` prints (contracts/doctor.schema.json). OK is true exactly when no finding is blocking.
type Doctor struct {
	SchemaVersion int             `json:"schema_version"`
	OK            bool            `json:"ok"`
	Binary        DoctorBinary    `json:"binary"`
	Findings      []DoctorFinding `json:"findings"`
}

type DoctorBinary struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// DoctorFinding is one thing doctor looked at. ID is stable (<check>.<outcome>); Fix is empty only for info.
type DoctorFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Subject  string `json:"subject"`
	Message  string `json:"message"`
	Fix      string `json:"fix"`
}

// Blocking says whether any finding stops a2dlc from doing its job as configured.
func (d Doctor) Blocking() bool {
	for _, f := range d.Findings {
		if f.Severity == DoctorBlocking {
			return true
		}
	}
	return false
}
