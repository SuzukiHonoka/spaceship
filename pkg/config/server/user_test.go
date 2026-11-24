package server

import "testing"

func TestUsersMatchIgnoresNilEntries(t *testing.T) {
	users := Users{nil, &User{UUID: "u-1"}, nil}
	if !users.Match("u-1") {
		t.Fatal("Users.Match missed valid user")
	}
	if users.Match("missing") {
		t.Fatal("Users.Match matched unknown user")
	}

	mm := users.ToMatchMap()
	if !mm.Match("u-1") {
		t.Fatal("UsersMatchMap.Match missed valid user")
	}
	if mm.Match("missing") {
		t.Fatal("UsersMatchMap.Match matched unknown user")
	}
}
