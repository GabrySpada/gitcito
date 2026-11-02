package keychain

// Service serves the renderer channels:
//   keychain:answer, keychain:status, keychain:set
// and the Go side emits keychain:ask (see Keychain.EnsureKeychain).
//
// It is a facade: every exported method here is callable from the page, so it
// exposes exactly the three channels the preload exposed and nothing else —
// in particular not Encrypt/Decrypt, which live on Keychain.

// Service is bound to the page.
type Service struct {
	k *Keychain
}

// NewService wraps k for binding.
func NewService(k *Keychain) *Service { return &Service{k: k} }

// Answer is `keychain:answer`: the user's reply to the explainer.
// channel: keychain:answer
func (s *Service) Answer(granted bool) { s.k.Answer(granted) }

// Status is `keychain:status`: {consent, explained, available}, available being
// null unless consent is granted.
// channel: keychain:status
func (s *Service) Status() Status { return s.k.Status() }

// Set is `keychain:set`: Settings → Security turning access on or off. Returns
// whether the keychain is usable afterwards.
// channel: keychain:set
func (s *Service) Set(granted bool) (bool, error) { return s.k.Set(granted) }
