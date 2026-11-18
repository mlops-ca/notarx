--
-- PostgreSQL database dump
--

\restrict oa0F7wP7MUSijb6XI6JQ2HIlvHzMeKoMThzvCFnfxz2azftN28ukDl8BIGv7Wq6

-- Dumped from database version 18.3
-- Dumped by pg_dump version 18.3

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: pg_stat_statements; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public;


--
-- Name: EXTENSION pg_stat_statements; Type: COMMENT; Schema: -; Owner:
--

COMMENT ON EXTENSION pg_stat_statements IS 'track planning and execution statistics of all SQL statements executed';


--
-- Name: pg_trgm; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;


--
-- Name: EXTENSION pg_trgm; Type: COMMENT; Schema: -; Owner:
--

COMMENT ON EXTENSION pg_trgm IS 'text similarity measurement and index searching based on trigrams';


--
-- Name: touch(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.touch() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;


ALTER FUNCTION public.touch() OWNER TO gateway;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.items (
    id uuid NOT NULL,
    name text NOT NULL
);


ALTER TABLE public.items OWNER TO gateway;

--
-- Name: notarx_schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notarx_schema_migrations (
    filename text NOT NULL,
    checksum text NOT NULL
);


ALTER TABLE public.notarx_schema_migrations OWNER TO gateway;

--
-- Name: schema_version; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_version (
    version integer NOT NULL
);


ALTER TABLE public.schema_version OWNER TO gateway;

--
-- Name: items items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.items
    ADD CONSTRAINT items_pkey PRIMARY KEY (id);


--
-- Name: schema_version schema_version_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_version
    ADD CONSTRAINT schema_version_pkey PRIMARY KEY (version);


--
-- Name: items_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX items_name_trgm_idx ON public.items USING gin (name public.gin_trgm_ops);


--
-- PostgreSQL database dump complete
--

\unrestrict oa0F7wP7MUSijb6XI6JQ2HIlvHzMeKoMThzvCFnfxz2azftN28ukDl8BIGv7Wq6

