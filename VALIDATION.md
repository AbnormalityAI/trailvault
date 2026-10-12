# Validation

12 October 2026 local build.

- Go 1.26.6 compiled the standalone binary; module targets Go 1.23.
- Ten Go tests passed with race detector: persistence/tampering, 30 concurrent appends, API auth/validation/filtering, malformed journal, unterminated otherwise-valid journal, strict unknown-field rejection, fail-closed /dev/full write errors, 100-row pagination and cursor, invalid-chain response and oversized-body 413. `go vet` passed.
- Browser interaction check: entered test token, appended an event via form, loaded it and verified the chain. No page JS errors.
- Actual screenshots inspected at 1280px desktop and 390px mobile. No horizontal overflow; controls, card text and long hashes readable. Test record is synthetic and runtime data is not shipped.
- Docker Compose recipe supplied, not executed here (Docker unavailable). Container claims are intentionally limited.
- Original banner rendered and visually inspected. CI supplied, not yet run remotely.
