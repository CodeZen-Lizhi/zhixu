package main

import "testing"

func TestDatabaseURLFromEnvironmentEscapesCredentials(t *testing.T) {
	values := map[string]string{
		"ZHIXU_DATABASE_HOST":     "postgres",
		"ZHIXU_DATABASE_PORT":     "5432",
		"ZHIXU_DATABASE_NAME":     "zhi xu",
		"ZHIXU_DATABASE_USER":     "user@local",
		"ZHIXU_DATABASE_PASSWORD": "p@ss/word#1",
	}
	value, err := databaseURLFromEnvironment(func(key string) (string, bool) {
		setting, found := values[key]
		return setting, found
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "postgres://user%40local:p%40ss%2Fword%231@postgres:5432/zhi%20xu?sslmode=disable"
	if value != want {
		t.Fatalf("database URL=%q, want %q", value, want)
	}
}

func TestDatabaseURLFromEnvironmentRejectsPartialConfiguration(t *testing.T) {
	if _, err := databaseURLFromEnvironment(func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("databaseURLFromEnvironment() accepted missing settings")
	}
}
