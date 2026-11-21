# Docs style guide

How to write Launchway's documentation so that it stays clear, honest and consistent.

## Honesty

- Describe only what is on the main branch. Anything else goes on the [roadmap](../roadmap-list.md)
  with its status: available, in progress or planned.
- Run every command you write, and say what you verified in the page's **Verified** note:

  ```text
  > **Verified** on <date> against commit <sha>: <what was run, where>. **Not verified:** <what was not>.
  ```

- Never write that Launchway is compliant with or certified against a standard. Describe controls.
- No comparisons with other products, and no marketing words such as "best", "seamless" or
  "world class".
- Use example names: `example.com`, `team/shop`, `payments`. Never real hosts or people.

## Language

- Plain words and short sentences. Define a term the first time, or link to the
  [glossary](../glossary.md).
- British spelling: organization, behavior, license (the noun).
- Second person and the imperative for instructions: "Create a file", not "The user creates".
- Say what happens and why, not only what to type.

## Page shape

- One `# Title`, then one or two sentences: what the page is for, and who it is for.
- Tutorials and how-to guides: *What you need*, numbered steps, *What you learned* (tutorials),
  *What next*.
- Headings in sentence case. Links between pages are relative paths to `.md` files, so they work
  on GitHub and on the site.

## Code and output

- Every code block has a language: `sh`, `yaml`, `json`, `text`, `go`.
- Shell blocks hold commands only, without a prompt. Show output in a separate `text` block, and
  copy it from a real run.

## Diagrams

- Mermaid in fenced `mermaid` blocks. GitHub and the site both render them.
- No colors, `style`, `classDef` or `%%{init}` themes: the site picks light or dark.
- Quote labels with punctuation: `A["Server (API)"]`. Keep to about fifteen nodes.

## Notes and warnings

GitHub's syntax, which the site renders too:

```text
> [!NOTE]
> [!TIP]
> [!IMPORTANT]
> [!WARNING]
> [!CAUTION]
```

## Images

Under `docs/images/<section>/`, compressed, referenced with relative paths and with alt text that
says what the image shows.
