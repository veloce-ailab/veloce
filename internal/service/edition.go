package service

// CurrentEdition identifies the build for clients.
//
// There is only one build. The former community/premium split was vestigial:
// the premium feature set was registered unconditionally at startup, so every
// "community edition" branch was unreachable. The value is kept because the
// public settings API exposes it to the frontend.
func CurrentEdition() string {
	return "premium"
}
