# Toolchain of the record-maven-fixtures workflow: Maven and Gradle on one JDK.
# Both source images are pinned by digest (docs/ci.md, "Re-record the Maven and
# Gradle fixtures"); bump the digests together when the clients change.
FROM maven:3.9-eclipse-temurin-21@sha256:99e61abcff91a9b1333463bd8451fb18495d6eba9250ac66a338b518f8278320 AS maven

FROM gradle:8.14-jdk21@sha256:ef0d44ca3acc3bd124a70943d4fa48bf7fec388d1a5dfc203d74e276a5b77314
COPY --from=maven /usr/share/maven /usr/share/maven
ENV MAVEN_HOME=/usr/share/maven
ENV PATH=/usr/share/maven/bin:$PATH
