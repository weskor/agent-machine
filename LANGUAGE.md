# Language

Shared architecture vocabulary for this repository. Use these terms consistently in architecture reviews and refactor plans.

## Terms

**Module**
Anything with an interface and an implementation. Deliberately scale-agnostic: applies equally to a function, package, command mode, or integration slice.
_Avoid_: unit, component, service.

**Interface**
Everything a caller must know to use a module correctly. Includes type signatures plus invariants, ordering constraints, error modes, required configuration, side effects, and performance characteristics.
_Avoid_: API, signature.

**Implementation**
What is inside a module. Use **Adapter** when the seam role is the topic; use implementation when the internal behavior is the topic.

**Depth**
Leverage at the interface: the amount of behavior a caller can exercise per unit of interface they must learn. A module is **deep** when a large amount of behavior sits behind a small interface. A module is **shallow** when the interface is nearly as complex as the implementation.

**Seam**
A place where behavior can vary without editing callers. Choosing where to put the seam is a design decision separate from what goes behind it.
_Avoid_: boundary when you mean interface location.

**Adapter**
A concrete thing that satisfies an interface at a seam. Describes role, not substance.

**Leverage**
What callers get from depth: more capability per unit of interface they must learn.

**Locality**
What maintainers get from depth: change, bugs, knowledge, and verification concentrate in one place instead of spreading through callers.

## Principles

- Depth is a property of the interface, not the implementation.
- The deletion test: if deleting a module makes complexity vanish, the module was not hiding much; if complexity reappears across callers, the module was earning its keep.
- The interface is the test surface. If tests must know too much past the interface, the module may be the wrong shape.
- One adapter means a hypothetical seam; two adapters means a real seam.
