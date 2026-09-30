package api

import "github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"

// authHashToken exposes the production hash function to the API tests.
func authHashToken(token string) string { return auth.HashToken(token) }
