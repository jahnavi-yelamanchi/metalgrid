// Command gen-token mints a local-dev HS256 JWT with a team claim, for
// exercising the API server's auth middleware without a real OIDC issuer.
// Usage: go run ./hack/gen-token -team platform -secret dev-secret
package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type teamClaims struct {
	Team string `json:"team"`
	jwt.RegisteredClaims
}

func main() {
	team := flag.String("team", "platform", "team claim to embed")
	secret := flag.String("secret", "dev-secret", "HS256 signing secret; must match JWT_SECRET")
	ttl := flag.Duration("ttl", time.Hour, "token lifetime")
	flag.Parse()

	claims := teamClaims{
		Team:             *team,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(*ttl))},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(*secret))
	if err != nil {
		panic(err)
	}
	fmt.Println(signed)
}
