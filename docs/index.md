---
hide:
  - toc
---

<div class="cox-home">
  <section class="cox-home__hero" aria-labelledby="cox-home-title">
    <span id="coxswain" aria-hidden="true"></span>
    <div class="cox-home__hero-copy">
      <p class="cox-kicker">Multi-agent epic orchestration</p>
      <h1 id="cox-home-title">Orchestrate epics. Not chats.</h1>
      <p>Coxswain gives one leader durable state, isolated workers, and explicit authority across every repository.</p>
      <div class="cox-home__actions">
        <a class="cox-button cox-button--primary" href="getting-started/install/">Install cox</a>
        <a class="cox-button cox-button--secondary" href="#why">See the model</a>
      </div>
    </div>
    <figure class="cox-diagram">
      <div class="cox-diagram__surface">
        <picture>
          <source media="(max-width: 719px)" srcset="assets/diagrams/orchestration-map-mobile.svg">
          <img src="assets/diagrams/orchestration-map.svg" alt="The captain directs one leader, which calls the cox core and adapters to coordinate isolated workers.">
        </picture>
      </div>
      <figcaption>Solid lines call a boundary. Blue dashed lines return durable status, checkpoints, and wakes through files. <a href="assets/diagrams/orchestration-map.svg">Open full size</a></figcaption>
      <details class="cox-diagram__text">
        <summary>Read the topology as text</summary>
        <p>The captain directs one leader session. The leader calls the harness-agnostic cox core. Cox reaches backend, harness, forge, and service boundaries through adapters, then coordinates one isolated worker per story and worktree. Workers return status and checkpoints through durable files, and the wake queue brings relevant change back to the leader.</p>
      </details>
    </figure>
  </section>

  <section class="cox-home__why" id="why" aria-labelledby="why-title">
    <h2 id="why-title">Work survives the session.</h2>
    <p class="cox-home__intro">A compacted prompt or restarted terminal does not erase the operational record.</p>
    <div class="cox-principles">
      <article class="cox-principle">
        <div>
          <h3>State is a file</h3>
          <p>Briefs, events, steering, and checkpoints remain inspectable on disk.</p>
        </div>
      </article>
      <article class="cox-principle">
        <div>
          <h3>Current state is derived</h3>
          <p>An append-only event log replaces guesses from conversation history.</p>
        </div>
      </article>
      <article class="cox-principle">
        <div>
          <h3>Boundaries are adapters</h3>
          <p>The core stays independent from backends, harnesses, forges, and services.</p>
        </div>
      </article>
      <article class="cox-principle">
        <div>
          <h3>Authority stays human</h3>
          <p>Only the captain signs the design, merges changes, and closes the epic.</p>
        </div>
      </article>
    </div>
  </section>

  <section class="cox-home__lifecycle" aria-labelledby="lifecycle-title">
    <div class="cox-section-copy">
      <h2 id="lifecycle-title">Every story has a visible state.</h2>
      <p>Transitions are events with an actor, an attempt, and evidence. Unknown never silently becomes clean.</p>
    </div>
    <figure class="cox-diagram">
      <div class="cox-diagram__surface">
        <picture>
          <source media="(max-width: 719px)" srcset="assets/diagrams/story-lifecycle-mobile.svg">
          <img src="assets/diagrams/story-lifecycle.svg" alt="A story moves from submitted to working, can pause for input or a checkpoint, and ends completed, failed, or canceled.">
        </picture>
      </div>
      <figcaption>A parked story resumes as a new attempt in the same worktree. Terminal outcomes retain their evidence. <a href="assets/diagrams/story-lifecycle.svg">Open full size</a></figcaption>
      <details class="cox-diagram__text">
        <summary>Read the lifecycle as text</summary>
        <p>Dispatch moves a submitted story to working. A worker can request input and continue after a reply. Parking requires a current checkpoint and a confirmed stop, then resume creates attempt N+1 in the same worktree. Completion requires captain-confirmed merge evidence. Failure and cancellation require a recorded reason.</p>
      </details>
    </figure>
  </section>

  <section class="cox-home__handoff" aria-labelledby="handoff-title">
    <div class="cox-section-head">
      <h2 id="handoff-title">Five lanes. One job each.</h2>
      <p>The terminal gets a doorbell. The durable file carries the meaning, acknowledgement, and recovery context.</p>
    </div>
    <figure class="cox-diagram">
      <div class="cox-diagram__surface">
        <picture>
          <source media="(max-width: 719px)" srcset="assets/diagrams/handoff-channels-mobile.svg">
          <img src="assets/diagrams/handoff-channels.svg" alt="Leader and worker exchange briefs, steering, control, reports, questions, and checkpoints through durable epic files.">
        </picture>
      </div>
      <figcaption>Gray moves work forward. Red invokes control. Blue returns status, questions, and checkpoint context. <a href="assets/diagrams/handoff-channels.svg">Open full size</a></figcaption>
      <details class="cox-diagram__text">
        <summary>Read the handoff as text</summary>
        <p>The leader dispatches a verbatim brief, adds direction through an atomic inbox record, and controls the worker only with allowlisted verbs. The worker reports status or questions into the epic and writes a checkpoint for its future session. Coxswain owns every handoff record while the backend provides only a worktree and terminal.</p>
      </details>
    </figure>
  </section>

  <section class="cox-home__arena" aria-labelledby="arena-title">
    <div>
      <h2 id="arena-title">Design review that can block.</h2>
      <p class="cox-arena__statement">Arena makes a separate harness challenge the design before workers inherit its mistakes.</p>
    </div>
    <ol class="cox-arena__steps">
      <li><span>01</span><div><h3>Challenge</h3><p>An adversary receives the same blinded context pack.</p></div></li>
      <li><span>02</span><div><h3>Verify</h3><p>Claims need machine-checked file, line, and revision evidence.</p></div></li>
      <li><span>03</span><div><h3>Synthesize</h3><p>The leader resolves contradictions and amends the design.</p></div></li>
      <li><span>04</span><div><h3>Sign</h3><p>The captain accepts the contract before dispatch begins.</p></div></li>
    </ol>
  </section>

  <section class="cox-home__start" id="start-here" aria-labelledby="start-title">
    <div>
      <h2 id="start-title">Run your first epic.</h2>
      <p class="cox-home__intro">Install the binary and plugin, verify the workspace, then follow the narrow first-epic route.</p>
      <div class="cox-runbook" aria-label="First commands">
        <code>cox workspace init --root ~/Work/my-workspace --repo app=/path/to/repo
cox epic new my-project my-first-epic --repo app --root ~/Work/my-workspace
cox epic stories --epic ~/Work/my-workspace/my-project/epics/my-first-epic</code>
      </div>
    </div>
    <nav class="cox-paths" aria-label="Start paths">
      <a class="cox-path" href="getting-started/install/"><span><strong>Install</strong><small>Orca, binary, harness, doctor</small></span></a>
      <a class="cox-path" href="getting-started/workspace/"><span><strong>Create a workspace</strong><small>One command, both shapes</small></span></a>
      <a class="cox-path" href="getting-started/first-epic/"><span><strong>First epic</strong><small>New, stories, dispatch</small></span></a>
      <a class="cox-path" href="getting-started/concepts/"><span><strong>Core concepts</strong><small>Captain, leader, worker, story</small></span></a>
    </nav>
  </section>
</div>
