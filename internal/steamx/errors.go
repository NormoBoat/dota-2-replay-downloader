package steamx

import "github.com/paralin/go-steam/protocol/steamlang"

// classifyLogin maps a rejected token logon: throttled is temporary, the rest needs `collector login`.
func classifyLogin(result steamlang.EResult) *LoginError {
	status := "auth_failed"
	switch result {
	case steamlang.EResult_RateLimitExceeded, steamlang.EResult_AccountLoginDeniedThrottle:
		status = "throttled"
	case steamlang.EResult_Banned, steamlang.EResult_AccountDisabled:
		status = "banned"
	}
	return &LoginError{Status: status, Message: "Steam logon rejected: " + result.String()}
}
