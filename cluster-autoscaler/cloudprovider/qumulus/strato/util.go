package stratocloud

import "github.com/google/uuid"

func UUID(s string) (string, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
