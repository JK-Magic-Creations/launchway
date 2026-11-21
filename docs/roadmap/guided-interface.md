# The guided interface

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › Guided interface · <a href="../roadmap-list.html">as a list</a></p>

An interface a complete beginner can use, that also keeps every detail an engineer needs. **Status:** guided setup wizards and a guided interface are available today in part (see the [demo](../demo.md)); the launch line and the rest below are in progress or planned in [Milestone 1](milestone-1.md#g-a-guided-interface-a-complete-beginner-can-use).

## Follow a change on its launch line

```mermaid
flowchart TD
  I["Idea<br/>work item"] --> R["Review"]
  R --> T["Build and test"]
  T --> P["Package"]
  P --> D["Test environment"]
  D --> S["Staging"]
  S --> Pr["Production"]
  Pr --> H["Verified healthy"]
```

Each station is a place the change has to pass. Choose a station and its evidence opens beneath the line: the review, the tests, the image and its contents, the place and its health, or a decision card.

## Overview, focus, detail

```mermaid
flowchart TD
  O["Overview<br/>what is going on, problems first"] --> F["Focus<br/>one thing, one sentence, one action"]
  F --> Dt["Detail<br/>everything for engineers"]
  Dt -.->|"links in context"| O
```

Every topic uses the same three levels, and each page answers one question. Pages link to each other where the question lives: a toggle links to its application and its change, an alert to the timeline, an image to where it runs.

## Design previews

These are static pictures of the design with sample data. They show the direction, not a released product.

![Design preview: Home says what needs you and shows changes on their way](../images/roadmap/design-preview-home.png)

*Design preview: Home.*

![Design preview: one change on its launch line, with everything connected to it](../images/roadmap/design-preview-journey.png)

*Design preview: the journey of one change.*

![Design preview: a rollout at 25 percent with checks and controls](../images/roadmap/design-preview-rollout.png)

*Design preview: a rollout in progress.*

## Principles

- Six plainly named sections. Anything switched off is not shown.
- Every task is a wizard of a few short screens that says what each is for.
- No page tells you to edit a file.
- Nothing the current interface shows is lost.

Go back to the [Roadmap](../public/roadmap.html).
