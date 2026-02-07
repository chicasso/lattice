package utils

func Retry(cb func(...any) any, maxTries, _backOff int) (any, error) {
	var err error = nil
	for range maxTries {
		resp := cb()

		switch resp := resp.(type) {
		case error:
			err = resp
		default:
			return resp, nil
		}
	}
	return nil, err
}
