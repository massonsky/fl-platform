package adapters

import "testing"

func TestAdministrativeInputBoundaries(t *testing.T) {
	disabled := false
	good := Input{Username: "researcher", Name: "Researcher", Role: "researcher", Password: "long-password-example"}
	if err := Validate("user.create", good); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Input){
		func(i *Input) { i.Username = "../admin" }, func(i *Input) { i.Role = "superuser" },
		func(i *Input) { i.Password = "short" }, func(i *Input) { i.Password = " padded password " },
		func(i *Input) { i.Disabled = &disabled }, func(i *Input) { i.Name = "   " },
	} {
		bad := good
		change(&bad)
		if Validate("user.create", bad) == nil {
			t.Fatal("invalid account accepted")
		}
	}
	if Validate("agent.update", Input{Disabled: &disabled}) != nil {
		t.Fatal("valid agent toggle rejected")
	}
	if Validate("agent.update", Input{Disabled: &disabled, Role: "admin"}) == nil {
		t.Fatal("agent role smuggling")
	}
	if Validate("user.update", Input{Role: "viewer"}) == nil {
		t.Fatal("missing disabled accepted")
	}
	if Validate("password.change", Input{Password: "a-long-new-password"}) == nil {
		t.Fatal("old password missing")
	}
}
