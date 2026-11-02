package domain

type APIError struct {
	Status        int
	Code, Message string
}
