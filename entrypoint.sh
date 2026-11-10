#!/bin/sh
set -eu
if [ "$(id -u)" -eq 0 ]; then
	owner=$(stat -c %u /data)
	group=$(stat -c %g /data)
	if [ "$owner" != "65532" ] || [ "$group" != "65532" ]; then
		chown 65532:65532 /data
	fi
	exec python3 -c 'import os,sys; os.setgid(65532); os.setuid(65532); os.execv("/usr/local/bin/kookbot", ["/usr/local/bin/kookbot"]+sys.argv[1:])' "$@"
fi
exec /usr/local/bin/kookbot "$@"
