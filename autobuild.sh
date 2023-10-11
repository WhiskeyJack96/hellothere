eval `ssh-agent -s` > /dev/null 2>&1
ssh-add ~/.ssh/github_rsa > /dev/null 2>&1

gh repo sync
go build -o hellotherebot main.go




