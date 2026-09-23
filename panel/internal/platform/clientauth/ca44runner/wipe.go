package ca44runner

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
