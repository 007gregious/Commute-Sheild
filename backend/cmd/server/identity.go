package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	identityHashBytes       = 32
	maxIdentityRequestBytes = 3 << 20
	maxSelfieBytes          = 2 << 20
	defaultSmileIDBaseURL   = "https://api.smileidentity.com"
	defaultSmileIDJobType   = "biometric_kyc"
)

var (
	vNINPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	errNameMismatch   = errors.New("verified names failed validation")
	errMissingSubject = errors.New("missing verified subject")
)

type identityConfig struct {
	SmileIDBaseURL       string
	SmileIDPartnerID     string
	SmileIDAPIKey        string
	SmileIDCallbackURL   string
	SmileIDJobType       string
	ErasureSigningSecret string
}

type identityRouter struct {
	db     *tracedDB
	smile  smileIDVerifier
	cfg    identityConfig
	tracer trace.Tracer
}

type smileIDVerifier interface {
	SubmitNIMCvNINBiometric(ctx context.Context, input smileNIMCVerificationInput) (smileVerificationResult, error)
}

type smileNIMCVerificationInput struct {
	UserID      string
	JobID       string
	VNIN        []byte
	SelfieImage []byte
}

type smileVerificationResult struct {
	FirstName string
	LastName  string
	DOB       string
	Verified  bool
}

type identityOnboardingRequest struct {
	UserID       string `json:"user_id"`
	VNIN         string `json:"vnin"`
	SelfieBase64 string `json:"selfie_base64"`
}

type identityOnboardingResponse struct {
	UserID       string `json:"user_id"`
	IdentityHash string `json:"identity_hash"`
	Verified     bool   `json:"verified"`
}

type erasureRequest struct {
	UserID string `json:"user_id"`
}

type erasureResponse struct {
	UserID string `json:"user_id"`
	Erased bool   `json:"erased"`
}

type smileIDRESTClient struct {
	baseURL     string
	partnerID   string
	apiKey      string
	callbackURL string
	jobType     string
	httpClient  *http.Client
}

func loadIdentityConfig() identityConfig {
	return identityConfig{
		SmileIDBaseURL:       env("SMILE_ID_BASE_URL", defaultSmileIDBaseURL),
		SmileIDPartnerID:     osEnvTrimmed("SMILE_ID_PARTNER_ID"),
		SmileIDAPIKey:        osEnvTrimmed("SMILE_ID_API_KEY"),
		SmileIDCallbackURL:   osEnvTrimmed("SMILE_ID_CALLBACK_URL"),
		SmileIDJobType:       env("SMILE_ID_JOB_TYPE", defaultSmileIDJobType),
		ErasureSigningSecret: osEnvTrimmed("ERASURE_SIGNING_SECRET"),
	}
}

func osEnvTrimmed(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func newSmileIDRESTClient(cfg identityConfig) *smileIDRESTClient {
	return &smileIDRESTClient{
		baseURL:     strings.TrimRight(cfg.SmileIDBaseURL, "/"),
		partnerID:   cfg.SmileIDPartnerID,
		apiKey:      cfg.SmileIDAPIKey,
		callbackURL: cfg.SmileIDCallbackURL,
		jobType:     cfg.SmileIDJobType,
		httpClient:  &http.Client{Timeout: 20 * time.Second},
	}
}

// SubmitNIMCvNINBiometric is the Smile ID SDK boundary for the router. Keep raw
// identity material on the stack of this call and return only vendor-verified
// fields needed to compute the data-minimized account hash.
func (c *smileIDRESTClient) SubmitNIMCvNINBiometric(ctx context.Context, input smileNIMCVerificationInput) (smileVerificationResult, error) {
	if c.partnerID == "" || c.apiKey == "" {
		return smileVerificationResult{}, errors.New("Smile ID credentials are not configured")
	}
	jobID := input.JobID
	if jobID == "" {
		jobID = fmt.Sprintf("identity-%s-%d", input.UserID, time.Now().UnixNano())
	}
	payload := map[string]any{
		"partner_id": c.partnerID,
		"source_sdk": "commuteshield-go-server",
		"user_id":    input.UserID,
		"job_id":     jobID,
		"job_type":   c.jobType,
		"country":    "NG",
		"id_info": map[string]string{
			"id_type":   "NIN_V2",
			"id_number": string(input.VNIN),
		},
		"images": []map[string]string{{
			"image_type_id": "2",
			"image":         base64.StdEncoding.EncodeToString(input.SelfieImage),
		}},
	}
	if c.callbackURL != "" {
		payload["callback_url"] = c.callbackURL
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return smileVerificationResult{}, err
	}
	defer purgeBytes(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/smile_id", bytes.NewReader(body))
	if err != nil {
		return smileVerificationResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return smileVerificationResult{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return smileVerificationResult{}, err
	}
	defer purgeBytes(respBody)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return smileVerificationResult{}, fmt.Errorf("Smile ID verification failed with HTTP %d", resp.StatusCode)
	}
	return parseSmileVerificationResponse(respBody)
}

func parseSmileVerificationResponse(respBody []byte) (smileVerificationResult, error) {
	var payload map[string]any
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return smileVerificationResult{}, err
	}
	first := firstString(payload, "first_name", "FirstName", "firstName", "full_data.first_name", "result.first_name", "Result.FirstName")
	last := firstString(payload, "last_name", "LastName", "lastName", "full_data.last_name", "result.last_name", "Result.LastName")
	dob := firstString(payload, "dob", "DOB", "date_of_birth", "DateOfBirth", "full_data.dob", "result.dob", "Result.DOB")
	verified := firstBool(payload, true, "verified", "Verified")
	if !verified {
		return smileVerificationResult{}, errors.New("Smile ID did not verify the identity")
	}
	if strings.TrimSpace(first) == "" || strings.TrimSpace(last) == "" || strings.TrimSpace(dob) == "" {
		return smileVerificationResult{}, errors.New("Smile ID response missing required identity fields")
	}
	return smileVerificationResult{FirstName: first, LastName: last, DOB: dob, Verified: verified}, nil
}

func (r *identityRouter) ServeOnboard(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, span := r.tracer.Start(req.Context(), "identity.onboard")
	defer span.End()

	input, selfieBytes, err := parseIdentityOnboardingRequest(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer purgeBytes(selfieBytes)
	vninBytes := []byte(input.VNIN)
	defer purgeBytes(vninBytes)
	span.SetAttributes(attribute.String("user.id", input.UserID))

	verified, err := r.smile.SubmitNIMCvNINBiometric(ctx, smileNIMCVerificationInput{
		UserID:      input.UserID,
		VNIN:        vninBytes,
		SelfieImage: selfieBytes,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "identity verification vendor rejected the payload")
		return
	}

	firstName := []byte(strings.TrimSpace(verified.FirstName))
	lastName := []byte(strings.TrimSpace(verified.LastName))
	dob := []byte(strings.TrimSpace(verified.DOB))
	defer purgeBytes(firstName)
	defer purgeBytes(lastName)
	defer purgeBytes(dob)
	verified.FirstName, verified.LastName, verified.DOB = "", "", ""

	if !validateVerifiedNames(firstName, lastName) {
		writeJSONError(w, http.StatusUnprocessableEntity, errNameMismatch.Error())
		return
	}
	identityHash := hashIdentity(firstName, lastName, dob)
	if len(identityHash) != identityHashBytes*2 {
		writeJSONError(w, http.StatusInternalServerError, "identity hash length invalid")
		return
	}
	if err := r.db.upsertIdentityHash(ctx, input.UserID, identityHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "user not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "persist identity hash")
		return
	}
	writeJSON(w, http.StatusOK, identityOnboardingResponse{UserID: input.UserID, IdentityHash: identityHash, Verified: true})
}

func (r *identityRouter) ServeErase(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost && req.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, span := r.tracer.Start(req.Context(), "identity.erase")
	defer span.End()
	var input erasureRequest
	if err := decodeJSON(req, &input); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.UserID = strings.TrimSpace(input.UserID)
	if input.UserID == "" {
		writeJSONError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if !validErasureSignature(req.Header.Get("x-erasure-signature"), input.UserID, r.cfg.ErasureSigningSecret) {
		writeJSONError(w, http.StatusUnauthorized, "invalid erasure signature")
		return
	}
	span.SetAttributes(attribute.String("user.id", input.UserID))
	erased, err := r.db.eraseVerifiedUser(ctx, input.UserID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "erase user")
		return
	}
	if !erased {
		writeJSONError(w, http.StatusNotFound, errMissingSubject.Error())
		return
	}
	writeJSON(w, http.StatusOK, erasureResponse{UserID: input.UserID, Erased: true})
}

func parseIdentityOnboardingRequest(req *http.Request) (identityOnboardingRequest, []byte, error) {
	var input identityOnboardingRequest
	if err := decodeJSON(req, &input); err != nil {
		return input, nil, err
	}
	input.UserID = strings.TrimSpace(input.UserID)
	input.VNIN = strings.TrimSpace(input.VNIN)
	input.SelfieBase64 = strings.TrimSpace(input.SelfieBase64)
	if input.UserID == "" {
		return input, nil, errors.New("user_id is required")
	}
	if !vNINPattern.MatchString(input.VNIN) {
		return input, nil, errors.New("vnin must be an 8-128 character platform-scoped token")
	}
	if input.SelfieBase64 == "" {
		return input, nil, errors.New("selfie_base64 is required")
	}
	selfie, err := base64.StdEncoding.DecodeString(input.SelfieBase64)
	if err != nil {
		selfie, err = base64.RawStdEncoding.DecodeString(input.SelfieBase64)
	}
	if err != nil {
		return input, nil, errors.New("selfie_base64 must be valid base64")
	}
	if len(selfie) == 0 || len(selfie) > maxSelfieBytes {
		purgeBytes(selfie)
		return input, nil, errors.New("selfie image size is invalid")
	}
	return input, selfie, nil
}

func decodeJSON(req *http.Request, dst any) error {
	defer req.Body.Close()
	dec := json.NewDecoder(io.LimitReader(req.Body, maxIdentityRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func validateVerifiedNames(firstName, lastName []byte) bool {
	return validHumanName(firstName) && validHumanName(lastName) && !bytes.Equal(bytes.ToLower(firstName), bytes.ToLower(lastName))
}

func validHumanName(name []byte) bool {
	trimmed := bytes.TrimSpace(name)
	if len(trimmed) < 2 || len(trimmed) > 80 || !utf8.Valid(trimmed) {
		return false
	}
	for _, r := range string(trimmed) {
		if unicode.IsLetter(r) || r == '-' || r == '\'' || r == ' ' {
			continue
		}
		return false
	}
	return true
}

func hashIdentity(firstName, lastName, dob []byte) string {
	h := sha256.New()
	h.Write(firstName)
	h.Write(lastName)
	h.Write(dob)
	sum := h.Sum(nil)
	defer purgeBytes(sum)
	return hex.EncodeToString(sum)
}

func (db *tracedDB) upsertIdentityHash(ctx context.Context, userID, identityHash string) error {
	ctx, span := db.tracer.Start(ctx, "db.users.identity_hash.upsert", trace.WithAttributes(attribute.String("user.id", userID)))
	defer span.End()
	commandTag, err := db.pool.Exec(ctx, `
		insert into commute.users (id, identity_hash, identity_verified_at, updated_at)
		values ($1, $2, now(), now())
		on conflict (id) do update
		set identity_hash = excluded.identity_hash,
		    identity_verified_at = excluded.identity_verified_at,
		    updated_at = excluded.updated_at
	`, userID, identityHash)
	if err == nil && commandTag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	recordSpanError(span, err)
	return err
}

func (db *tracedDB) eraseVerifiedUser(ctx context.Context, userID string) (bool, error) {
	tx, err := db.begin(ctx)
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	var identityHash *string
	err = tx.QueryRow(ctx, `
		select identity_hash
		from commute.users
		where id = $1
		for update
	`, userID).Scan(&identityHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if identityHash == nil || *identityHash == "" {
		return false, nil
	}
	commandTag, err := tx.Exec(ctx, `delete from commute.profiles where id = $1`, userID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	committed = true
	return commandTag.RowsAffected() == 1, nil
}

func validErasureSignature(provided, userID, secret string) bool {
	provided = strings.TrimSpace(provided)
	if provided == "" || secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(userID))
	expected := []byte(hex.EncodeToString(mac.Sum(nil)))
	defer purgeBytes(expected)
	return hmac.Equal([]byte(provided), expected)
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

func purgeBytes(buf []byte) {
	for i := range buf {
		buf[i] = 0
	}
}

func firstString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := nestedValue(payload, key); ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

func firstBool(payload map[string]any, fallback bool, keys ...string) bool {
	for _, key := range keys {
		if value, ok := nestedValue(payload, key); ok {
			if parsed, ok := value.(bool); ok {
				return parsed
			}
			if text, ok := value.(string); ok {
				switch strings.ToLower(strings.TrimSpace(text)) {
				case "true", "verified", "success", "approved":
					return true
				case "false", "failed", "rejected":
					return false
				}
			}
		}
	}
	return fallback
}

func nestedValue(payload map[string]any, dotted string) (any, bool) {
	current := any(payload)
	for _, part := range strings.Split(dotted, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
