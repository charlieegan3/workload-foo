FILE_PATTERN := 'html\|go\|Makefile\|css'

dev_server:
	find . | grep $(FILE_PATTERN) | entr -r bash -c 'clear; go run main.go server --config config.dev.yaml'
