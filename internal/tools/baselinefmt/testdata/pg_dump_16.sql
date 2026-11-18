--
-- PostgreSQL database dump
--

\restrict auMHoQU03aJUyOE4gEqnxvdg0YdtwxpjzjR3m8rLcjbNCbfTIdqiZTVGrEb9tXC

-- Dumped from database version 16.16
-- Dumped by pg_dump version 16.16

SET statement_timeout = 0;
SET client_encoding = 'UTF8';
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;

--
-- Name: pg_stat_statements; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public;


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

--
-- Name: items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.items (
    id uuid NOT NULL,
    name text NOT NULL
);


--
-- Name: notarx_schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notarx_schema_migrations (
    filename text NOT NULL,
    checksum text NOT NULL
);


--
-- Name: schema_version; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_version (
    version integer NOT NULL
);


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

\unrestrict auMHoQU03aJUyOE4gEqnxvdg0YdtwxpjzjR3m8rLcjbNCbfTIdqiZTVGrEb9tXC

