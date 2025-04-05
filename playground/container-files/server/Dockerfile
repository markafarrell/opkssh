FROM linuxserver/openssh-server:latest

RUN apk add openssh-server

COPY opkssh /usr/local/bin/opkssh

COPY container-files/server/ /

RUN groupadd --system opksshuser && \
    useradd -r -M -s /sbin/nologin -g "opksshuser" "opksshuser" && \
    chown root:opksshuser /usr/local/bin/opkssh && \
    chmod +x /usr/local/bin/opkssh && \
    chown root:opksshuser /etc/opk/auth_id && \
    chmod 640 /etc/opk/auth_id && \
    chown root:opksshuser /etc/opk/providers && \
    chmod 640 /etc/opk/providers && \
    chmod 440 /etc/sudoers.d/opkssh && \
    touch /var/log/opkssh.log && \
    chown root:opksshuser /var/log/opkssh.log && \
    chmod 660 /var/log/opkssh.log 

RUN useradd --user-group --create-home bob && \
    passwd -d bob

ARG GOOGLE_USER_EMAIL
RUN /usr/local/bin/opkssh add bob ${GOOGLE_USER_EMAIL} google

ARG MICROSOFT_USER_EMAIL
RUN /usr/local/bin/opkssh add bob ${MICROSOFT_USER_EMAIL} azure

ARG GITLAB_USER_EMAIL
RUN /usr/local/bin/opkssh add bob ${GITLAB_USER_EMAIL} gitlab
