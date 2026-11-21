import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";
import vocab from "../vocabulary.json";
import { setVocabulary, type WordDef } from "../terms";

// The words are a table the app loads at start; every test starts with it installed.
setVocabulary(vocab.words as WordDef[], vocab.groups);

afterEach(() => cleanup());

// jsdom does not implement scrolling.
window.scrollTo = () => {};
