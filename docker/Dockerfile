FROM ubuntu:jammy

EXPOSE 7000
EXPOSE 6000

COPY nixy nixy

USER root
RUN apt-get update && apt-get install -y nginx gettext

COPY docker-nixy.toml.subst .
# COPY docker-nginx.tmpl.subst .
COPY nginx.tmpl .
COPY entrypoint.sh entrypoint.sh
RUN chmod 711 entrypoint.sh

ENTRYPOINT [ "./entrypoint.sh" ]