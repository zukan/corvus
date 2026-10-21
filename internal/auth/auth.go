package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Error carries the HTTP status and message expected by the legacy API.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Claims contains the tenant identity and the standard JWT timestamps.
type Claims struct {
	ApplicationID  string `json:"applicationId,omitempty"`
	OrganizationID string `json:"organizationId,omitempty"`
	AdminID        string `json:"adminId,omitempty"`
	jwt.RegisteredClaims
}

// Service signs and validates tenant-scoped JWTs.
type Service struct {
	applicationKey      []byte
	organizationKey     []byte
	ttl                 time.Duration
	credentialsRequired bool
}

func New(appSecret, orgSecret string, ttl time.Duration, credentialsRequired bool) (*Service, error) {
	if ttl <= 0 {
		return nil, errors.New("token ttl must be positive")
	}
	app, err := decodeSecret(appSecret)
	if err != nil {
		return nil, fmt.Errorf("invalid application secret: %w", err)
	}
	org, err := decodeSecret(orgSecret)
	if err != nil {
		return nil, fmt.Errorf("invalid organization secret: %w", err)
	}
	return &Service{applicationKey: app, organizationKey: org, ttl: ttl, credentialsRequired: credentialsRequired}, nil
}

func decodeSecret(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(key) == 0 {
		return nil, errors.New("secret must not be empty")
	}
	return key, nil
}

func (s *Service) Parse(header, kind string) (Claims, error) {
	var claims Claims
	if strings.TrimSpace(header) == "" {
		if !s.credentialsRequired {
			return claims, nil
		}
		return claims, &Error{Status: 401, Message: "No authorization token was found"}
	}
	parts := strings.Fields(header)
	if len(parts) != 2 {
		return claims, &Error{Status: 400, Message: "Format is Authorization: Bearer [token]"}
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return claims, &Error{Status: 401, Message: "Unauthorized"}
	}
	key, err := s.keyFor(kind)
	if err != nil {
		return claims, err
	}
	token, err := jwt.ParseWithClaims(parts[1], &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || token == nil || !token.Valid {
		return Claims{}, &Error{Status: 401, Message: jwtErrorMessage(token, err)}
	}
	return claims, nil
}

func jwtErrorMessage(token *jwt.Token, err error) string {
	switch {
	case errors.Is(err, jwt.ErrTokenMalformed):
		return "jwt malformed"
	case errors.Is(err, jwt.ErrTokenExpired):
		return "jwt expired"
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return "jwt not active"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid) && token != nil && token.Method != jwt.SigningMethodHS256:
		return "invalid algorithm"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return "invalid signature"
	default:
		return "Unauthorized"
	}
}

func (s *Service) keyFor(kind string) ([]byte, error) {
	switch strings.ToLower(kind) {
	case "application":
		return s.applicationKey, nil
	case "organization":
		return s.organizationKey, nil
	default:
		return nil, &Error{Status: 401, Message: "Unauthorized"}
	}
}

func (s *Service) SignApplication(appID, adminID string) (string, error) {
	return s.sign(s.applicationKey, Claims{ApplicationID: appID, AdminID: adminID})
}

func (s *Service) SignOrganization(orgID, adminID string) (string, error) {
	return s.sign(s.organizationKey, Claims{OrganizationID: orgID, AdminID: adminID})
}

func (s *Service) sign(key []byte, claims Claims) (string, error) {
	now := time.Now()
	claims.IssuedAt = jwt.NewNumericDate(now)
	claims.ExpiresAt = jwt.NewNumericDate(now.Add(s.ttl))
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
}

func HashPassword(password string) (string, error) {
	if len([]byte(password)) > 72 {
		return "", errors.New("password exceeds bcrypt's 72-byte limit")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func ComparePassword(hash, password string) error {
	if len([]byte(password)) > 72 {
		return errors.New("password exceeds bcrypt's 72-byte limit")
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
