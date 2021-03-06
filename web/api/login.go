package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	jwtService "github.com/madappgang/identifo/jwt/service"
	"github.com/madappgang/identifo/model"
	thp "github.com/madappgang/identifo/user_payload_provider/http"
	"github.com/madappgang/identifo/web/authorization"
	"github.com/madappgang/identifo/web/middleware"
	"github.com/xlzd/gotp"
)

var (
	errPleaseEnableTFA   = fmt.Errorf("please enable two-factor authentication to be able to use this app")
	errPleaseSetPhoneTFA = fmt.Errorf("please set phone for two-factor authentication to be able to use this app")
	errPleaseSetEmailTFA = fmt.Errorf("please set email for two-factor authentication to be able to use this app")
	errPleaseDisableTFA  = fmt.Errorf("please disable two-factor authentication to be able to use this app")
)

const (
	smsTFACode        = "%v is your one-time password!"
	hotpLifespanHours = 12 // One time code expiration in hours, default value is 30 secs for TOTP and 12 hours for HOTP
)

// AuthResponse is a response with successful auth data.
type AuthResponse struct {
	AccessToken  string     `json:"access_token,omitempty" bson:"access_token,omitempty"`
	RefreshToken string     `json:"refresh_token,omitempty" bson:"refresh_token,omitempty"`
	User         model.User `json:"user,omitempty" bson:"user,omitempty"`
	Require2FA   bool       `json:"require_2fa" bson:"require_2fa"`
	Enabled2FA   bool       `json:"enabled_2fa" bson:"enabled_2fa"`
}

type loginData struct {
	Username    string   `json:"username,omitempty"`
	Password    string   `json:"password,omitempty"`
	DeviceToken string   `json:"device_token,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
}

func (ld *loginData) validate() error {
	usernameLen := len(ld.Username)
	if usernameLen < 6 || usernameLen > 130 {
		return fmt.Errorf("incorrect username length %d, expected a number between 6 and 130", usernameLen)
	}
	pswdLen := len(ld.Password)
	if pswdLen < 6 || pswdLen > 130 {
		return fmt.Errorf("incorrect password length %d, expected a number between 6 and 130", pswdLen)
	}
	return nil
}

// LoginWithPassword logs user in with username and password.
func (ar *Router) LoginWithPassword() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ar.SupportedLoginWays.Username {
			ar.Error(w, ErrorAPIAppLoginWithUsernameNotSupported, http.StatusBadRequest, "Application does not support login with username", "LoginWithPassword.supportedLoginWays")
			return
		}

		ld := loginData{}
		if ar.MustParseJSON(w, r, &ld) != nil {
			return
		}

		if err := ld.validate(); err != nil {
			ar.Error(w, ErrorAPIRequestBodyParamsInvalid, http.StatusBadRequest, err.Error(), "LoginWithPassword.validate")
			return
		}

		user, err := ar.userStorage.UserByNamePassword(ld.Username, ld.Password)
		if err != nil {
			ar.Error(w, ErrorAPIRequestIncorrectEmailOrPassword, http.StatusUnauthorized, err.Error(), "LoginWithPassword.UserByNamePassword")
			return
		}

		app := middleware.AppFromContext(r.Context())
		if len(app.ID) == 0 {
			ar.logger.Println("Error getting App")
			ar.Error(w, ErrorAPIRequestAppIDInvalid, http.StatusBadRequest, "App is not in context.", "LoginWithPassword.AppFromContext")
			return
		}

		// Authorize user if the app requires authorization.
		azi := authorization.AuthzInfo{
			App:         app,
			UserRole:    user.AccessRole,
			ResourceURI: r.RequestURI,
			Method:      r.Method,
		}
		if err := ar.Authorizer.Authorize(azi); err != nil {
			ar.Error(w, ErrorAPIAppAccessDenied, http.StatusForbidden, err.Error(), "LoginWithPassword.Authorizer")
			return
		}

		authResult, err := ar.loginFlow(app, user, ld.Scopes)
		if err != nil {
			ar.Error(w, ErrorAPIInternalServerError, http.StatusInternalServerError, err.Error(), "LoginWithPassword.LoginFlowError")
			return
		}

		ar.ServeJSON(w, http.StatusOK, authResult)
	}
}

func (ar *Router) sendOTPCode(user model.User) error {
	// we don't need to send any code for FTA Type App, it uses TOTP and generated on client side with the app
	if ar.tfaType != model.TFATypeApp {

		// increment hotp code seed
		otp := gotp.NewDefaultHOTP(user.TFAInfo.Secret).At(user.TFAInfo.HOTPCounter + 1)
		tfa := user.TFAInfo
		tfa.HOTPCounter++
		tfa.HOTPExpiredAt = time.Now().Add(time.Hour * hotpLifespanHours)
		user.TFAInfo = tfa
		if _, err := ar.userStorage.UpdateUser(user.ID, user); err != nil {
			return err
		}
		switch ar.tfaType {
		case model.TFATypeSMS:
			return ar.sendTFACodeInSMS(user.Phone, otp)
		case model.TFATypeEmail:
			return ar.sendTFACodeOnEmail(user.Email, otp)
		}

	}

	return nil
}

// IsLoggedIn is for checking whether user is logged in or not.
// In fact, all needed work is done in Token middleware.
// If we reached this code, user is logged in (presented valid and not blacklisted access token).
func (ar *Router) IsLoggedIn() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ar.ServeJSON(w, http.StatusOK, nil)
	}
}

// GetUser return current user info with sanitized tfa
func (ar *Router) GetUser() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := tokenFromContext(r.Context()).UserID()
		user, err := ar.userStorage.UserByID(userID)
		if err != nil {
			ar.Error(w, ErrorAPIUserNotFound, http.StatusUnauthorized, err.Error(), "UpdateUser.UserByID")
			return
		}
		ar.ServeJSON(w, http.StatusOK, user.SanitizedTFA())
	}

}

// getTokenPayloadForApp get additional token payload data
func (ar *Router) getTokenPayloadForApp(app model.AppData, user model.User) (map[string]interface{}, error) {
	if app.TokenPayloadService == model.TokenPayloadServiceHttp {
		// check if we have service cached
		ps, exists := ar.tokenPayloadServices[app.ID]
		if !exists {
			var err error
			ps, err = thp.NewTokenPayloadProvider(
				app.TokenPayloadServiceHttpSettings.Secret,
				app.TokenPayloadServiceHttpSettings.URL,
			)
			if err != nil {
				return nil, err
			}
			ar.tokenPayloadServices[app.ID] = ps
		}
		return ps.TokenPayloadForApp(app.ID, app.Name, user.ID)
	}
	return nil, nil
}

// loginUser creates and returns access token for a user.
// createRefreshToken boolean param tells if we should issue refresh token as well.
func (ar *Router) loginUser(user model.User, scopes []string, app model.AppData, createRefreshToken, require2FA bool, tokenPayload map[string]interface{}) (accessTokenString, refreshTokenString string, err error) {
	token, err := ar.tokenService.NewAccessToken(user, scopes, app, require2FA, tokenPayload)
	if err != nil {
		return
	}

	accessTokenString, err = ar.tokenService.String(token)
	if err != nil {
		return
	}
	if !createRefreshToken || require2FA {
		return
	}

	refresh, err := ar.tokenService.NewRefreshToken(user, scopes, app)
	if err != nil {
		return
	}
	refreshTokenString, err = ar.tokenService.String(refresh)
	if err != nil {
		return
	}
	return
}

// check2FA checks correspondence between app's TFAstatus and user's TFAInfo,
// and decides if we require two-factor authentication after all checks are successfully passed.
func (ar *Router) check2FA(appTFAStatus model.TFAStatus, serverTFAType model.TFAType, user model.User) (bool, bool, error) {
	if appTFAStatus == model.TFAStatusMandatory && !user.TFAInfo.IsEnabled {
		return true, false, errPleaseEnableTFA
	}

	if appTFAStatus == model.TFAStatusDisabled && user.TFAInfo.IsEnabled {
		return false, true, errPleaseDisableTFA
	}

	// Request two-factor auth if user enabled it and app supports it.
	if user.TFAInfo.IsEnabled && appTFAStatus != model.TFAStatusDisabled {
		if user.Phone == "" && serverTFAType == model.TFATypeSMS {
			// Server required sms tfa but user phone is empty
			return true, false, errPleaseSetPhoneTFA
		}
		if user.Email == "" && serverTFAType == model.TFATypeEmail {
			// Server required email tfa but user email is empty
			return true, false, errPleaseSetEmailTFA
		}
		if user.TFAInfo.Secret == "" {
			// Then admin must have enabled TFA for this user manually.
			// User must obtain TFA secret, i.e send EnableTFA request.
			return true, false, errPleaseEnableTFA
		}
		return true, true, nil
	}
	return false, false, nil
}

func (ar *Router) sendTFACodeInSMS(phone, otp string) error {
	if phone == "" {
		return errors.New("unable to send SMS OTP, user has no phone number")
	}

	if err := ar.smsService.SendSMS(phone, fmt.Sprintf(smsTFACode, otp)); err != nil {
		return fmt.Errorf("unable to send sms. %s", err)
	}
	return nil
}

func (ar *Router) sendTFACodeOnEmail(email, otp string) error {
	if email == "" {
		return errors.New("unable to send email OTP, user has no email")
	}

	if err := ar.emailService.SendTFAEmail("One-time password", email, otp); err != nil {
		return fmt.Errorf("unable to send email with OTP with error: %s", err)
	}
	return nil
}

func (ar *Router) loginFlow(app model.AppData, user model.User, scopes []string) (AuthResponse, error) {
	// Do login flow.
	scopes, err := ar.userStorage.RequestScopes(user.ID, scopes)
	if err != nil {
		return AuthResponse{}, err
	}

	// Check if we should require user to authenticate with 2FA.
	require2FA, enabled2FA, err := ar.check2FA(app.TFAStatus, ar.tfaType, user)
	if !require2FA && enabled2FA && err != nil {
		return AuthResponse{}, err
	}

	offline := contains(scopes, jwtService.OfflineScope)

	tokenPayload, err := ar.getTokenPayloadForApp(app, user)
	if err != nil {
		return AuthResponse{}, err
	}

	accessToken, refreshToken, err := ar.loginUser(user, scopes, app, offline, require2FA, tokenPayload)
	if err != nil {
		return AuthResponse{}, err
	}

	result := AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		Require2FA:   require2FA,
		Enabled2FA:   enabled2FA,
	}

	if require2FA && enabled2FA {
		if err := ar.sendOTPCode(user); err != nil {
			return AuthResponse{}, err
		}
	} else {
		ar.userStorage.UpdateLoginMetadata(user.ID)
	}

	user = user.Sanitized()
	result.User = user
	return result, nil
}
