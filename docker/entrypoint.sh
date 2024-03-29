#!/bin/bash
#set -x

pid=0

# SIGTERM-handler
term_handler() {
  if [ $pid -ne 0 ]; then
    kill -SIGTERM "$pid"
    wait "$pid"
  fi
  echo "Exiting on sigterm"
  exit 143; # 128 + 15 -- SIGTERM
}

# setup handlers
# on callback, kill the last background process, which is `tail -f /dev/null` and execute the specified handler
trap 'kill ${!}; term_handler' SIGTERM

if [ -z "${DROVE_CONTROLLERS}" ]; then
    echo "Error: DROVE_CONTROLLER is a mandatory parameter for nixy to work."
    exit 1
fi
IFS=',' read -r -a hosts <<< "${DROVE_CONTROLLERS}"
export DROVE_CONTROLLER_LIST=$(for host in ${hosts[@]}; do echo "\"$host\""; done|paste -sd ',' -)
export DROVE_USERNAME="${DROVE_USERNAME:-guest}"
export DROVE_PASSWORD="${DROVE_PASSWORD:-guest}"

export NGINX_NUM_WORKERS=${NGINX_NUM_WORKERS:-2}
export TEMPLATE_PATH=${TEMPLATE_FILE_PATH:-./nginx.tmpl}


DEBUG_ENABLED="${DEBUG:-0}"
if [ "$DEBUG_ENABLED" -ne 0 ]; then

  echo "Environment variables:"
  printenv


  echo "Contents of working dir: ${PWD}"
  ls -l "${PWD}"

fi

envsubst > nixy.toml < docker-nixy.toml.subst
# envsubst > nginx.tmpl < docker-nginx.tmpl.subst

CONFIG_PATH=${CONFIG_FILE_PATH:-/nixy.toml}

if [ ! -f "${CONFIG_PATH}" ]; then
  echo "Config file ${CONFIG_PATH} not found."
  echo "File system:"
  ls -l "$(dirname ${CONFIG_PATH})" 
  exit 1
else
  echo "Config ${CONFIG_PATH} file exists. Proceeding to service startup."
fi


if [ ! -f "${TEMPLATE_PATH}" ]; then
  echo "Template file ${TEMPLATE_PATH} not found."
  echo "File system:"
  ls -l /
  exit 1
else
  echo "Config ${TEMPLATE_PATH} file exists. Proceeding to service startup."
fi

if [ "$DEBUG_ENABLED" -ne 0 ]; then
  cat "${CONFIG_PATH}"

  cat "${TEMPLATE_PATH}"
fi
# run application
CMD=$(eval echo "./nixy -f ${CONFIG_PATH}")
echo "Starting Nixy by running command: ${CMD}"

service nginx start
eval "${CMD}" &

pid="$!"

# wait forever
while true
do
  tail -f /dev/null & wait ${!}
done


