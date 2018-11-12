package mongo

import (
	"encoding/json"

	"github.com/madappgang/identifo/model"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/mgo.v2/bson"
)

const (
	//UsersCollection collection name for users
	UsersCollection = "Users"
)

//NewUserStorage creates and inits mongodb user storage
func NewUserStorage(db *DB) (model.UserStorage, error) {
	us := UserStorage{}
	us.db = db
	//TODO: ensure indexes
	s := us.db.Session(UsersCollection)
	defer s.Close()

	if err := s.C.EnsureIndexKey("name"); err != nil {
		return nil, err
	}

	return &us, nil
}

//UserStorage implements user storage in memory
type UserStorage struct {
	db *DB
}

//UserByID returns user by it's ID
func (us *UserStorage) UserByID(id string) (model.User, error) {
	if !bson.IsObjectIdHex(id) {
		return nil, model.ErrorWrongDataFormat
	}
	s := us.db.Session(UsersCollection)
	defer s.Close()

	var u userData
	if err := s.C.FindId(bson.ObjectIdHex(id)).One(&u); err != nil {
		return nil, err
	}
	return &User{userData: u}, nil
}

//UserByFederatedID returns user by federated ID
func (us *UserStorage) UserByFederatedID(provider model.FederatedIdentityProvider, id string) (model.User, error) {
	s := us.db.Session(UsersCollection)
	defer s.Close()
	sid := string(provider) + ":" + id

	var u userData
	if err := s.C.Find(bson.M{"federatedIDs": sid}).One(&u); err != nil {
		return nil, model.ErrorNotFound
	}
	//clear password hash
	u.Pswd = ""
	return &User{userData: u}, nil
}

//CheckIfUserExistByName checks if user exist with presented name.
func (us *UserStorage) CheckIfUserExistByName(name string) bool {
	s := us.db.Session(UsersCollection)
	defer s.Close()

	q := bson.M{"$regex": bson.RegEx{Pattern: createStrictRegex(name), Options: "i"}}
	var u userData
	err := s.C.Find(bson.M{"name": q}).One(&u)

	return err == nil
}

//AttachDeviceToken do nothing here
//TODO: implement device storage
func (us *UserStorage) AttachDeviceToken(id, token string) error {
	//we are not supporting devices for users here
	return model.ErrorNotImplemented
}

//DetachDeviceToken do nothing here yet
//TODO: implement
func (us *UserStorage) DetachDeviceToken(token string) error {
	return model.ErrorNotImplemented
}

//RequestScopes for now returns requested scope
//TODO: implement scope logic
func (us *UserStorage) RequestScopes(userID string, scopes []string) ([]string, error) {
	return scopes, nil
}

//UserByNamePassword returns  user by name and password
func (us *UserStorage) UserByNamePassword(name, password string) (model.User, error) {
	s := us.db.Session(UsersCollection)
	defer s.Close()

	var u userData
	q := bson.M{"$regex": bson.RegEx{Pattern: name, Options: "i"}}
	if err := s.C.Find(bson.M{"name": q}).One(&u); err != nil {
		return nil, model.ErrorNotFound
	}
	if bcrypt.CompareHashAndPassword([]byte(u.Pswd), []byte(password)) != nil {
		return nil, model.ErrorNotFound
	}
	//clear password hash
	u.Pswd = ""
	return &User{userData: u}, nil
}

//AddNewUser adds new user
func (us *UserStorage) AddNewUser(usr model.User, password string) (model.User, error) {
	u, ok := usr.(*User)
	if !ok {
		return nil, model.ErrorWrongDataFormat
	}
	s := us.db.Session(UsersCollection)
	defer s.Close()
	u.userData.ID = bson.NewObjectId()
	if len(password) > 0 {
		u.userData.Pswd = PasswordHash(password)
	}
	if err := s.C.Insert(u.userData); err != nil {
		return nil, err
	}
	return u, nil
}

//AddUserByNameAndPassword register new user
func (us *UserStorage) AddUserByNameAndPassword(name, password string, profile map[string]interface{}) (model.User, error) {
	//using user name as a key
	_, err := us.UserByID(name)
	//if there is no error, it means user already exists
	if err == nil {
		return nil, model.ErrorUserExists
	}
	u := userData{}
	u.Active = true
	u.Name = name
	u.Profile = profile
	return us.AddNewUser(&User{u}, password)
}

//AddUserWithFederatedID add new user with social ID
func (us *UserStorage) AddUserWithFederatedID(provider model.FederatedIdentityProvider, federatedID string) (model.User, error) {
	_, err := us.UserByFederatedID(provider, federatedID)
	//if there is no error, it means user already exists
	if err == nil {
		return nil, model.ErrorUserExists
	}
	sid := string(provider) + ":" + federatedID
	u := userData{}
	u.Active = true
	u.Name = sid
	u.FederatedIDs = []string{sid}
	return us.AddNewUser(&User{u}, "")

}

//data implementation
type userData struct {
	ID           bson.ObjectId          `bson:"_id,omitempty" json:"id,omitempty"`
	Name         string                 `bson:"name,omitempty" json:"name,omitempty"`
	Pswd         string                 `bson:"pswd,omitempty" json:"pswd,omitempty"`
	Profile      map[string]interface{} `bson:"profile,omitempty" json:"profile,omitempty"`
	Active       bool                   `bson:"active,omitempty" json:"active,omitempty"`
	FederatedIDs []string               `bson:"deferated_ids,omitempty" json:"deferated_ids,omitempty"`
}

//User user data structure for mongodb storage
type User struct {
	userData
}

//Sanitize removes sensitive data
func (u *User) Sanitize() {
	u.userData.Pswd = ""
	u.userData.Active = false
}

//UserFromJSON deserializes data
func UserFromJSON(d []byte) (*User, error) {
	user := userData{}
	if err := json.Unmarshal(d, &user); err != nil {
		return &User{}, err
	}
	return &User{user}, nil
}

//model.User interface implementation
func (u *User) ID() string                      { return u.userData.ID.Hex() }
func (u *User) Name() string                    { return u.userData.Name }
func (u *User) PasswordHash() string            { return u.userData.Pswd }
func (u *User) Profile() map[string]interface{} { return u.userData.Profile }
func (u *User) Active() bool                    { return u.userData.Active }

//PasswordHash creates hash with salt for password
func PasswordHash(pwd string) string {
	hash, _ := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
	return string(hash)
}

func createStrictRegex(str string) string {
	return "^" + str + "$"
}
